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
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"suse.com/virtx/pkg/ha"
	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

/* ha_state_of renders one switch state for the client. */
func ha_state_of(st ha.State) openapi.HaState {
	var (
		o openapi.HaState
	)
	o.Enabled = st.Enabled
	o.Generation = st.Generation
	o.Applied = st.Applied
	o.Acked = st.Acked
	o.Missing = st.Missing
	o.Pending = st.Pending
	o.Names = ha_names_of(append(append([]string(nil), st.Acked...), st.Missing...))
	return o
}

/*
 * ha_names_of is what the inventory calls the hosts of a state, so that a client
 * can say which host is behind instead of printing a uuid and leaving the reader
 * to look it up.
 */
func ha_names_of(uuids []string) map[string]string {
	var (
		names map[string]string = make(map[string]string)
		info inventory.HostInfo
		err error
	)
	for _, uuid := range uuids {
		info, err = inventory.Get_hostinfo(uuid)
		if (err == nil && info.Name != "") {
			names[uuid] = info.Name
		}
	}
	return names
}

/* ha_response encodes one switch state and answers with it. */
func ha_response(w http.ResponseWriter, st ha.State) {
	var (
		o openapi.HaState
		buf bytes.Buffer
		err error
	)
	o = ha_state_of(st)
	err = json.NewEncoder(&buf).Encode(&o)
	if (err != nil) {
		logger.Log("ha: could not encode the state: %s", err.Error())
		http.Error(w, "failed to encode JSON", http.StatusInternalServerError)
		return
	}
	httpx.Do_response(w, http.StatusOK, &buf)
}

/*
 * ha_get answers GET /ha: the switch of the action surface, and which hosts
 * have applied the generation it belongs to.
 */
func ha_get(w http.ResponseWriter, r *http.Request) {
	ha_response(w, ha.Get())
}

/*
 * ha_put answers PUT /ha?enabled=0|1. It writes the switch and waits for the
 * hosts to apply it.
 *
 * Only the coordinator writes: a host that does not hold the office answers 409
 * and says what it sees about the office, so that the operator learns where the
 * command has to go instead of getting a change that silently lost a race.
 *
 * A change that not everybody has applied yet is not an error: the answer says
 * which host is behind, and the change is still there to be finished - by the
 * coordinator on its next round, or by whoever takes the office next.
 */
func ha_put(w http.ResponseWriter, r *http.Request) {
	var (
		arg string
		enable bool
		st ha.State
		err error
	)
	arg = r.URL.Query().Get("enabled")
	if (arg == "") {
		http.Error(w, "no enabled=0|1 given", http.StatusBadRequest)
		return
	}
	enable, err = strconv.ParseBool(arg)
	if (err != nil) {
		http.Error(w, "enabled must be 0|1", http.StatusBadRequest)
		return
	}
	if (enable) {
		st, err = ha.Enable()
	} else {
		st, err = ha.Disable()
	}
	if (err != nil) {
		if (errors.Is(err, ha.Err_not_coordinator)) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		logger.Log("ha: %s", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ha_response(w, st)
}
