/*
===========================================================================

wiring_publication.go - capture scene recipients under the authority lock.

Delivery runs after the lock is released, retaining each scene revision.
===========================================================================
*/
package main

import (
	log "github.com/sirupsen/logrus"
	"opensro.online/server/internal/game/item/wire"
	"opensro.online/server/internal/transport"
)

/*
================
captureCharacterFrames

The sessions bound to one character name (captureSessionFrames).
================
*/
func captureCharacterFrames(hub *transport.Hub, division, name string) func([]wire.Frame) {
	return captureSessionFrames(hub.CharacterSessions(division, name))
}

/*
================
captureObjectFrames

The sessions bound to one player object id (captureSessionFrames).
================
*/
func captureObjectFrames(hub *transport.Hub, division string, gid uint32) func([]wire.Frame) {
	var sessions []*transport.Session
	for _, session := range hub.SessionsInDivision(division) {
		if bound, ok := session.CharacterObjectID(); ok && bound == gid {
			sessions = append(sessions, session)
		}
	}
	return captureSessionFrames(sessions)
}

/*
================
captureSessionFrames

Capture is read-only under the action division lock. The returned delivery
runs outside that lock so queue failure can synchronously retire gameplay.
================
*/
func captureSessionFrames(sessions []*transport.Session) func([]wire.Frame) {
	type recipient struct {
		session  *transport.Session
		revision uint64
	}
	var recipients []recipient
	for _, session := range sessions {
		revision, valid := session.SceneReceiptRevision()
		if valid {
			recipients = append(recipients, recipient{session: session, revision: revision})
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	return func(frames []wire.Frame) {
		batch := make([]transport.Frame, len(frames))
		for i, frame := range frames {
			batch[i] = transport.Frame{Opcode: frame.Opcode, Payload: frame.Payload, Current: frame.Current, Scope: transport.ScopeChanges(frame.Scope)}
		}
		for _, recipient := range recipients {
			if err := recipient.session.SendSceneReceiptBatch(recipient.revision, batch); err != nil {
				log.WithError(err).WithField("session", recipient.session.ID).Debug("action: captured private receipt refused")
			}
		}
	}
}
