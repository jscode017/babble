package node

import (
	"fmt"
	"time"

	hg "github.com/mosaicnetworks/babble/src/hashgraph"
	"github.com/mosaicnetworks/babble/src/net"
	"github.com/sirupsen/logrus"
)

func (n *Node) requestSync(target string, known map[uint32]int) (net.SyncResponse, error) {
	args := net.SyncRequest{
		FromID: n.id,
		Known:  known,
	}

	var out net.SyncResponse

	err := n.trans.Sync(target, &args, &out)

	return out, err
}

func (n *Node) requestEagerSync(target string, events []hg.WireEvent) (net.EagerSyncResponse, error) {
	args := net.EagerSyncRequest{
		FromID: n.id,
		Events: events,
	}

	var out net.EagerSyncResponse

	err := n.trans.EagerSync(target, &args, &out)

	return out, err
}

func (n *Node) requestFastForward(target string) (net.FastForwardResponse, error) {
	n.logger.WithFields(logrus.Fields{
		"target": target,
	}).Debug("RequestFastForward()")

	args := net.FastForwardRequest{
		FromID: n.id,
	}

	var out net.FastForwardResponse

	err := n.trans.FastForward(target, &args, &out)

	return out, err
}

func (n *Node) processRPC(rpc net.RPC) {
	switch cmd := rpc.Command.(type) {
	case *net.SyncRequest:
		n.processSyncRequest(rpc, cmd)
	case *net.EagerSyncRequest:
		n.processEagerSyncRequest(rpc, cmd)
	case *net.FastForwardRequest:
		n.processFastForwardRequest(rpc, cmd)
	default:
		n.logger.WithField("cmd", rpc.Command).Error("Unexpected RPC command")
		rpc.Respond(nil, fmt.Errorf("unexpected command"))
	}
}

func (n *Node) processSyncRequest(rpc net.RPC, cmd *net.SyncRequest) {
	n.logger.WithFields(logrus.Fields{
		"from_id": cmd.FromID,
		"known":   cmd.Known,
	}).Debug("process SyncRequest")

	resp := &net.SyncResponse{
		FromID: n.id,
	}

	var respErr error

	//Check sync limit
	n.coreLock.Lock()
	overSyncLimit := n.core.OverSyncLimit(cmd.Known, n.conf.SyncLimit)
	n.coreLock.Unlock()

	if overSyncLimit {
		n.logger.Debug("SyncLimit")
		resp.SyncLimit = true
	} else {
		//Compute Diff
		start := time.Now()
		n.coreLock.Lock()
		eventDiff, err := n.core.EventDiff(cmd.Known)
		n.coreLock.Unlock()
		elapsed := time.Since(start)

		n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("Diff()")

		if err != nil {
			n.logger.WithField("error", err).Error("Calculating Diff")
			respErr = err
		}

		//Convert to WireEvents
		wireEvents, err := n.core.ToWire(eventDiff)
		if err != nil {
			n.logger.WithField("error", err).Debug("Converting to WireEvent")
			respErr = err
		} else {
			resp.Events = wireEvents
		}
	}

	//Get Self Known
	n.coreLock.Lock()
	knownEvents := n.core.KnownEvents()
	n.coreLock.Unlock()

	resp.Known = knownEvents

	n.logger.WithFields(logrus.Fields{
		"events":     len(resp.Events),
		"known":      resp.Known,
		"sync_limit": resp.SyncLimit,
		"rpc_err":    respErr,
	}).Debug("Responding to SyncRequest")

	rpc.Respond(resp, respErr)
}

func (n *Node) processEagerSyncRequest(rpc net.RPC, cmd *net.EagerSyncRequest) {
	n.logger.WithFields(logrus.Fields{
		"from_id": cmd.FromID,
		"events":  len(cmd.Events),
	}).Debug("EagerSyncRequest")

	success := true

	n.coreLock.Lock()
	err := n.sync(cmd.FromID, cmd.Events)
	n.coreLock.Unlock()

	if err != nil {
		n.logger.WithField("error", err).Error("sync()")
		success = false
	}

	resp := &net.EagerSyncResponse{
		FromID:  n.id,
		Success: success,
	}

	rpc.Respond(resp, err)
}

func (n *Node) processFastForwardRequest(rpc net.RPC, cmd *net.FastForwardRequest) {
	n.logger.WithFields(logrus.Fields{
		"from": cmd.FromID,
	}).Debug("process FastForwardRequest")

	resp := &net.FastForwardResponse{
		FromID: n.id,
	}

	var respErr error

	//Get latest Frame
	n.coreLock.Lock()
	block, frame, err := n.core.GetAnchorBlockWithFrame()
	n.coreLock.Unlock()

	if err != nil {
		n.logger.WithField("error", err).Error("Getting Frame")
		respErr = err
	} else {
		resp.Block = *block
		resp.Frame = *frame

		//Get snapshot
		snapshot, err := n.proxy.GetSnapshot(block.Index())

		if err != nil {
			n.logger.WithField("error", err).Error("Getting Snapshot")
			respErr = err
		} else {
			resp.Snapshot = snapshot
		}
	}

	n.logger.WithFields(logrus.Fields{
		"events":  len(resp.Frame.Events),
		"rpc_err": respErr,
	}).Debug("Responding to FastForwardRequest")

	rpc.Respond(resp, respErr)
}
