/*
 * Copyright (c) 2026 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */
package virtx

import (
	"net/http"
	"os"

	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/reg"
	"suse.com/virtx/pkg/ts"
	"suse.com/virtx/pkg/vmlog"
)

/*
 * vm_takeover is the actuator of a MANUAL takeover: it moves a VM that belongs
 * to a host that is no longer active to the requested host, and starts it there.
 *
 * It deliberately makes no judgement about whether it is safe to take over:
 *   - the operator is expected to have checked the disk leases first
 *     (for example with virtx-ha-probe.sh can-boot);
 *   - the storage and libvirt remain the arbiters:
 *     if the previous owner's process is gone while its leases have not expired
 *     yet, the boot below does not fail: it waits for the lease to expire
 *     (host_dead) and only then starts the VM. If another LIVE writer still
 *     holds the disk image, the boot fails on qemu's image write lock and the
 *     takeover rolls back (measured 2026-10-03: "Failed to get "write" lock",
 *     the registration is moved back, and no second writer appears either way).
 *     In particular a host that is merely network partitioned still renews its
 *     leases on the shared storage, so the takeover cannot start a second
 *     writer.
 *
 * A VM whose owning host is still active in the cluster is refused, and a
 * takeover is only ever executed by the host that is going to run the VM.
 *
 * Taking a VM over is a manual action: it is not a judgement of the decision
 * layer, and it is therefore not gated by the switch of the action surface. The
 * operator who asks for it is the one who decides, and the checks below are what
 * keeps the decision honest.
 *
 * Because the boot can wait for the old owner's leases (tens of seconds), the
 * request returns 202 as soon as the checks passed, like vm_migrate does, and
 * the outcome is recorded in the VM's log: "virtx log vm <uuid>".
 */
func vm_takeover(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		uuid string
		o openapi.VmRegisterOptions
		vr httpx.Request
		vminfo inventory.VmInfo
		hostinfo inventory.HostInfo
		old_host string
		old_state openapi.Vmrunstate
		client_ip string
		msg string
		ts_start int64
		dry_run bool
	)
	vr, err = httpx.Decode_request_body(r, &o)
	if (err != nil) {
		logger.Log("%s", err.Error())
		http.Error(w, "failed to decode body", http.StatusBadRequest)
		return
	}
	uuid = r.PathValue("uuid")
	if (uuid == "") {
		http.Error(w, "could not get uuid", http.StatusBadRequest)
		return
	}
	if (o.Host == "") {
		http.Error(w, "no target host given", http.StatusBadRequest)
		return
	}
	if (http_host_is_remote(o.Host)) {
		/* a takeover has to be executed by the host that will run the VM */
		http_proxy_request(o.Host, w, vr)
		return
	}
	vminfo, err = inventory.Get_vminfo(uuid)
	if (err != nil) {
		http.Error(w, "unknown uuid", http.StatusNotFound)
		return
	}
	old_host = vminfo.Host
	old_state = vminfo.Runstate
	if (old_host == machine.Uuid()) {
		/* the VM is already registered here: nothing to take over */
		httpx.Do_response(w, http.StatusNoContent, nil)
		return
	}
	/*
	 * never touch a VM whose owning host is still alive: it is either still
	 * writing to its disks (then the boot below would wait for it), or we would
	 * be guessing. A manual takeover is only for a host the cluster already
	 * considers gone.
	 */
	hostinfo, err = inventory.Get_hostinfo(old_host)
	if (err != nil) {
		logger.Log("vm_takeover: no information about the owning host: %s", err.Error())
		http.Error(w, "unknown owning host", http.StatusFailedDependency)
		return
	}
	if (hostinfo.Cstate == openapi.CSTATE_ACTIVE) {
		http.Error(w, "the owning host is still active", http.StatusUnprocessableEntity)
		return
	}
	/* the registration must be where the cluster view says it is */
	err = reg.Access(old_host, uuid)
	if (err != nil) {
		if (os.IsNotExist(err)) {
			http.Error(w, "no registration under the owning host", http.StatusFailedDependency)
		} else {
			logger.Log("vm_takeover: reg.Access failed: %s", err.Error())
			http.Error(w, "failed to check registration", http.StatusInternalServerError)
		}
		return
	}
	client_ip = httpx.Client_ip(r)
	/*
	 * A drill is requested with dry_run=1: the landing does the move and undoes
	 * it again, and never defines or starts anything. It is a query parameter
	 * on purpose - the generated model is not touched.
	 */
	dry_run = (r.URL.Query().Get("dry_run") == "1")
	msg = "takeover from " + hostinfo.Name
	if (dry_run) {
		msg = "dry-run takeover from " + hostinfo.Name
	}
	ts_start = ts.Now()
	go func() {
		vm_takeover_run(uuid, old_host, old_state, hostinfo.Name, client_ip, msg, ts_start, dry_run)
	}()
	httpx.Do_response(w, http.StatusAccepted, nil)
}

/*
 * vm_takeover_run does the actual work, off the request, so that a boot waiting
 * for the old owner's disk leases cannot make the client give up on a takeover
 * that is still going to succeed.
 */
func vm_takeover_run(uuid string, old_host string, old_state openapi.Vmrunstate, old_host_name string, client_ip string, msg string, ts_start int64, dry_run bool) {
	var (
		err error
		xml, canonical_xml string
		defined bool
	)
	/* move the whole per-VM registration directory to this host */
	err = reg.Move(machine.Uuid(), old_host, uuid)
	if (err != nil) {
		logger.Log("vm_takeover: reg.Move failed: %s", err.Error())
		return
	}
	if (dry_run) {
		/*
		 * A drill: the move itself is real - it is the step the whole ordering
		 * exists for - and it is undone again, so a drill leaves the cluster
		 * exactly as it found it. Nothing past this point may run: no inventory
		 * update, no domain definition, no start.
		 */
		/*
		 * The oplog entry is written while the registration is still on this
		 * host: after the move back, the directory it writes into belongs to
		 * the old host again and the write fails (measured 2026-10-04: the dry
		 * run left no oplog line, with "no such file or directory").
		 */
		err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_COMPLETED,
			client_ip, msg, "Dry run: landing accepted, registration moved back.", ts_start)
		if (err != nil) {
			logger.Log("vm_takeover: dry-run: vmlog failed: %s", err.Error())
		}
		err = reg.Move(old_host, machine.Uuid(), uuid)
		if (err != nil) {
			logger.Log("vm_takeover: dry-run: moving the registration back failed: %s", err.Error())
			vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start, err.Error())
			return
		}
		logger.Log("vm_takeover: dry-run: %s: landing accepted, registration moved back", uuid)
		return
	}
	/*
	 * correct our own view of the cluster: the VM is here now and it is not
	 * running. Without this, vm_boot() would refuse: it only starts domains
	 * that the inventory reports as POWEROFF or CRASHED, and it would proxy
	 * the request to the old (gone) host.
	 */
	err = inventory.Update_vm_state(&inventory.VmEvent{
		Uuid: uuid,
		Host: machine.Uuid(),
		Runstate: openapi.RUNSTATE_POWEROFF,
		Ts: ts.Now(),
	})
	if (err != nil) {
		logger.Log("vm_takeover: inventory update failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start, err.Error())
		return
	}
	/* define the domain here, from the registration we just moved */
	xml, err = reg.Load(machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("vm_takeover: reg.Load failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start, err.Error())
		return
	}
	canonical_xml, err = hypervisor.Define_domain(xml)
	if (err != nil) {
		logger.Log("vm_takeover: Define_domain failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start, err.Error())
		return
	}
	defined = true
	if (canonical_xml != xml) {
		/*
		 * libvirt may canonicalize the XML differently from what is stored in
		 * the registry; save it back so the registry matches libvirt. A
		 * failure here is non-fatal, as in vm_register.
		 */
		err = reg.Save(machine.Uuid(), uuid, canonical_xml)
		if (err != nil) {
			logger.Log("vm_takeover: reg.Save failed: %s", err.Error())
		}
	}
	/*
	 * start it. This is where sanlock and qemu take the disk leases: if the
	 * previous owner still holds them, the boot waits for it to be dead.
	 */
	_, err = hypervisor.Boot_domain(uuid, &openapi.VmBootOptions{})
	if (err != nil) {
		logger.Log("vm_takeover: Boot_domain failed: %s", err.Error())
		vm_takeover_fail(uuid, old_host, old_state, defined, client_ip, msg, ts_start, err.Error())
		return
	}
	err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_COMPLETED, client_ip, msg, "Taken over.", ts_start)
	if (err != nil) {
		logger.Log("vm_takeover: oplog: %s", err.Error())
	}
	logger.Debug("takeover of domain %s from %s successful", uuid, old_host_name)
}

/*
 * vm_takeover_fail records a failed takeover in the VM's log while the VM
 * directory is still on this host, and then puts everything back the way it
 * was: the domain definition (only if we created it here), the per-VM
 * registration directory, and our view of the cluster.
 */
func vm_takeover_fail(uuid string, old_host string, old_state openapi.Vmrunstate, defined bool, client_ip string, msg string, ts_start int64, reason string) {
	var (
		err error
	)
	err = vmlog.StartEnd(uuid, openapi.OpVmRegister, openapi.OPERATION_FAILED, client_ip, msg, reason, ts_start)
	if (err != nil) {
		logger.Log("vm_takeover: oplog: %s", err.Error())
	}
	if (defined) {
		err = hypervisor.Undefine_domain(uuid)
		if (err != nil) {
			logger.Log("vm_takeover: rollback: Undefine_domain failed: %s", err.Error())
		}
	}
	err = reg.Move(old_host, machine.Uuid(), uuid)
	if (err != nil) {
		logger.Log("vm_takeover: rollback: reg.Move failed: %s", err.Error())
	}
	err = inventory.Update_vm_state(&inventory.VmEvent{
		Uuid: uuid,
		Host: old_host,
		Runstate: old_state,
		Ts: ts.Now(),
	})
	if (err != nil) {
		logger.Log("vm_takeover: rollback: inventory update failed: %s", err.Error())
	}
}
