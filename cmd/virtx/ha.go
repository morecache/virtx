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
}
