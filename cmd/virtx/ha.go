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

package main

import (
	"fmt"

	"suse.com/virtx/pkg/model"
)

func ha_get_req() {
	virtx.path = "/ha"
	virtx.method = "GET"
	virtx.arg = nil
	virtx.result = &openapi.HaState{}
}

/*
 * ha_set_req asks for a change of the switch. The parameter travels as a query
 * string, like the other parameters this tree added after the model was
 * generated: the model is not touched for it.
 */
func ha_set_req(enabled bool) {
	virtx.path = fmt.Sprintf("/ha?enabled=%v", enabled)
	virtx.method = "PUT"
	virtx.arg = nil
	virtx.result = &openapi.HaState{}
}

/* ha_show prints one switch state: the answer to get, enable and disable alike. */
func ha_show(st *openapi.HaState) {
	var (
		surface string = "off"
	)
	if (st.Enabled) {
		surface = "on"
	}
	fmt.Fprintf(virtx.w, "HA RESPONSE\t%s\n", surface)
	fmt.Fprintf(virtx.w, "GENERATION\t%d\n", st.Generation)
	fmt.Fprintf(virtx.w, "APPLIED HERE\t%d\n", st.Applied)
	fmt.Fprintf(virtx.w, "ACKED\t%d\n", len(st.Acked))
	if (st.Pending) {
		fmt.Fprintf(virtx.w, "PENDING\tyes\n")
	}
	fmt.Fprintf(virtx.w, "NOT YET\t%d\n", len(st.Missing))
	for _, uuid := range st.Missing {
		if (st.Names[uuid] != "") {
			fmt.Fprintf(virtx.w, "  waiting for\t%s (%s)\n", uuid, st.Names[uuid])
			continue
		}
		fmt.Fprintf(virtx.w, "  waiting for\t%s\n", uuid)
	}
	/*
	 * What this host concluded about its peers. Only what asks for something is
	 * printed: a cluster where every judgement is a no-op stays as quiet as it
	 * was before this line existed.
	 */
	for _, j := range st.Judgements {
		if (j.Verdict == "" || j.Verdict == "no-op") {
			continue
		}
		fmt.Fprintf(virtx.w, "%s\n", ha_judgement_line(j))
	}
}

/*
 * ha_judgement_line is the one line an operator reads about one judgement, in
 * the words of what it asks for: an alert is for a person, a hold is a decision
 * the cluster is not taking, and an action is one it did take.
 */
func ha_judgement_line(j openapi.HaJudgement) string {
	var (
		what string = "hold"
		text string
	)
	if (j.Verdict == "alert-only") {
		what = "alert"
	}
	if (j.Verdict == "restart") {
		what = "action"
	}
	text = fmt.Sprintf("%s\t%s: %s", what, j.Name, j.Row)
	if (j.Report != "") {
		text += fmt.Sprintf(", report %s", j.Report)
	}
	if (j.Lease != "") {
		text += fmt.Sprintf(", lease %s", j.Lease)
	}
	if (!j.Mgmt_known) {
		text += ", mgmt unknown"
	} else if (j.Mgmt_reachable) {
		text += ", mgmt reachable"
	} else {
		text += ", mgmt unreachable"
	}
	text += fmt.Sprintf(", age %ds", j.Age_ms / 1000)
	if (j.E1 != "" && j.E1 != "n/a") {
		text += fmt.Sprintf(", e1=%s", j.E1)
	}
	if (len(j.Isolation_stopped_vms) != 0) {
		text += fmt.Sprintf(", stopped %d vm(s)", len(j.Isolation_stopped_vms))
	}
	if (j.Row == "Partitioned") {
		text += " - needs an operator"
	}
	return text
}
