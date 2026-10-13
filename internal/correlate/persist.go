// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package correlate

import (
	"database/sql"
)

const instCols = `id, host, inst, qid, origin, state, first_ts, last_ts, COALESCE(accepted_ts,0), accept_observed,
	COALESCE(removed_ts,0), sender, COALESCE(size,0), COALESCE(nrcpt,0), COALESCE(message_id,''), has_message_id,
	COALESCE(client,''), COALESCE(ip,''), COALESCE(sasl_method,''), COALESCE(sasl_user,''), COALESCE(uid,-1),
	COALESCE(parent_id,0), COALESCE(root_id,0), COALESCE(notif_type,''), COALESCE(notif_parent_id,0), filtered,
	COALESCE(expired_ts,0), flags`

func scanInstance(rows *sql.Rows) (*instance, error) {
	in := &instance{rcpts: map[string]*recipient{}}
	var sender sql.NullString
	var flags string
	err := rows.Scan(&in.id, &in.host, &in.inst, &in.qid, &in.origin, &in.state, &in.firstTS, &in.lastTS, &in.acceptedTS,
		&in.acceptObserved, &in.removedTS, &sender, &in.size, &in.nrcpt, &in.messageID, &in.hasMessageID, &in.client, &in.ip,
		&in.saslMethod, &in.saslUser, &in.uid, &in.parentID, &in.rootID, &in.notifType, &in.notifParentID, &in.filtered,
		&in.expiredTS, &flags)
	if err != nil {
		return nil, err
	}
	in.sender, in.hasSender = sender.String, sender.Valid
	in.flags = splitFlags(flags)
	// A stored instance with client/pickup data had its start observed.
	in.hasStart = in.client != "" || in.ip != "" || in.uid >= 0
	return in, nil
}

func (hs *hostState) saveInstance(in *instance) error {
	var sender any
	if in.hasSender {
		sender = in.sender
	}
	var uid any
	if in.uid >= 0 {
		uid = in.uid
	}
	_, err := hs.exec(`UPDATE instances SET origin=?, state=?, first_ts=?, last_ts=?, accepted_ts=?, accept_observed=?,
		removed_ts=?, sender=?, size=?, nrcpt=?, message_id=?, has_message_id=?, client=?, ip=?, sasl_method=?, sasl_user=?,
		uid=?, parent_id=?, root_id=?, notif_type=?, notif_parent_id=?, filtered=?, expired_ts=?, flags=? WHERE id=?`,
		in.origin, in.state, in.firstTS, in.lastTS, nullInt(in.acceptedTS), in.acceptObserved, nullInt(in.removedTS), sender,
		nullInt(in.size), nullInt(int64(in.nrcpt)), nullStr(in.messageID), in.hasMessageID, nullStr(in.client), nullStr(in.ip),
		nullStr(in.saslMethod), nullStr(in.saslUser), uid, nullInt(in.parentID), nullInt(in.rootID), nullStr(in.notifType),
		nullInt(in.notifParentID), in.filtered, nullInt(in.expiredTS), joinFlags(in.flags), in.id)
	return err
}
