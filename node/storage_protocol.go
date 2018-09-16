package node

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	inet "gx/ipfs/QmQSbtGXCyNrj34LWL8EgXyNNYDZ8r3SwQcpW5pPxVhLnM/go-libp2p-net"
	cbor "gx/ipfs/QmV6BQ6fFCf9eFHDuRxvguvqfKLZtZrxthgZvDfRCs4tMN/go-ipld-cbor"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	ipld "gx/ipfs/QmX5CsuHyVZeTLxgRSYkgLSDQKb9UjE8xnhQzCEJWWWFsC/go-ipld-format"
	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
	"gx/ipfs/QmZNkThpqfVXs9GNbexPrfBbXSLNYeKrE7jwFM2oqHbyqN/go-libp2p-protocol"
	unixfs "gx/ipfs/Qmdg2crJzNUF1mLPnLPSCCaDdLDqE4Qrh9QEiDooSYkvuB/go-unixfs"

	dag "gx/ipfs/QmeLG6jF1xvEmHca5Vy4q4EdQWp8Xq9S6EPyZrN9wvSRLC/go-merkledag"

	"github.com/filecoin-project/go-filecoin/actor/builtin/miner"
	"github.com/filecoin-project/go-filecoin/address"
	cbu "github.com/filecoin-project/go-filecoin/cborutil"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/types"
	vmErrors "github.com/filecoin-project/go-filecoin/vm/errors"
)

const StorageDealProtocolID = protocol.ID("/fil/storage/mk/1.0.0")       // nolint: golint
const StorageDealQueryProtocolID = protocol.ID("/fil/storage/qry/1.0.0") // nolint: golint

func init() {
	cbor.RegisterCborType(StorageDealProposal{})
	cbor.RegisterCborType(StorageDealResponse{})
	cbor.RegisterCborType(PaymentInfo{})
	cbor.RegisterCborType(ProofInfo{})
	cbor.RegisterCborType(storageDealQueryRequest{})
}

// StorageDealProposal is
type StorageDealProposal struct {
	// PieceRef is the cid of the piece being stored
	PieceRef *cid.Cid

	// Size is the total number of bytes the proposal is asking to store
	Size *types.BytesAmount

	// TotalPrice is the total price that will be paid for the entire storage operation
	TotalPrice *types.AttoFIL

	// Duration is the number of blocks to make a deal for
	Duration uint64

	// Payment PaymentInfo
	// Signature types.Signature
}

// PaymentInfo is
type PaymentInfo struct{}

// StorageDealResponse is
type StorageDealResponse struct {
	// State is the current state of this deal
	State DealState

	// Message is an optional message to add context to any given response
	Message string

	// Proposal is the cid of the StorageDealProposal object this response is for
	Proposal *cid.Cid

	// ProofInfo is a collection of information needed to convince the client that
	// the miner has sealed the data into a sector.
	//ProofInfo *ProofInfo

	// Signature is a signature from the miner over the response
	Signature types.Signature
}

// ProofInfo is proof info
type ProofInfo struct {
}

// StorageMiner represents a storage miner
type StorageMiner struct {
	nd *Node

	minerAddr      address.Address
	minerOwnerAddr address.Address

	deals   map[string]*storageDealState
	dealsLk sync.Mutex

	postInProcessLk sync.Mutex
	postInProcess   *types.BlockHeight

	dealsAwaitingSeal   map[uint64][]*cid.Cid
	dealsAwaitingSealLk sync.Mutex
}

type storageDealState struct {
	proposal *StorageDealProposal

	state *StorageDealResponse
}

// NewStorageMiner is
func NewStorageMiner(ctx context.Context, nd *Node, minerAddr, minerOwnerAddr address.Address) (*StorageMiner, error) {
	sm := &StorageMiner{
		nd:                nd,
		minerAddr:         minerAddr,
		minerOwnerAddr:    minerOwnerAddr,
		deals:             make(map[string]*storageDealState),
		dealsAwaitingSeal: make(map[uint64][]*cid.Cid),
	}
	nd.Host.SetStreamHandler(StorageDealProtocolID, sm.handleProposalStream)
	nd.Host.SetStreamHandler(StorageDealQueryProtocolID, sm.handleQuery)

	return sm, nil
}

func (sm *StorageMiner) handleProposalStream(s inet.Stream) {
	defer s.Close() // nolint: errcheck

	var proposal StorageDealProposal
	if err := cbu.NewMsgReader(s).ReadMsg(&proposal); err != nil {
		panic(err)
	}

	ctx := context.Background()
	resp, err := sm.ReceiveStorageProposal(ctx, &proposal)
	if err != nil {
		panic(err)
	}

	if err := cbu.NewMsgWriter(s).WriteMsg(resp); err != nil {
		panic(err)
	}
}

// ReceiveStorageProposal is the entry point for the miner storage protocol
func (sm *StorageMiner) ReceiveStorageProposal(ctx context.Context, p *StorageDealProposal) (*StorageDealResponse, error) {
	// TODO: Check signature

	// TODO: check size, duration, totalprice match up with the payment info
	//       and also check that the payment info is valid.
	//       A valid payment info contains enough funds to *us* to cover the totalprice

	// TODO: decide if we want to accept this thingy

	// Payment is valid, everything else checks out, let's accept this proposal
	return sm.acceptProposal(ctx, p)
}

func (sm *StorageMiner) acceptProposal(ctx context.Context, p *StorageDealProposal) (*StorageDealResponse, error) {
	if sm.sectorBuilder() == nil {
		return nil, errors.New("Mining disabled, can not proccess proposal")
	}

	// TODO: we don't really actually want to put this in our general storage
	// but we just want to get its cid, as a way to uniquely track it
	propcid, err := sm.nd.CborStore.Put(ctx, p)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cid of proposal")
	}

	resp := &StorageDealResponse{
		State:     Accepted,
		Proposal:  propcid,
		Signature: types.Signature("signaturrreee"),
	}

	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	sm.deals[propcid.KeyString()] = &storageDealState{
		proposal: p,
		state:    resp,
	}

	// TODO: use some sort of nicer scheduler
	go sm.processStorageDeal(propcid)

	return resp, nil
}

func (sm *StorageMiner) getStorageDeal(c *cid.Cid) *storageDealState {
	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	return sm.deals[c.KeyString()]
}

func (sm *StorageMiner) updateDealState(c *cid.Cid, f func(*StorageDealResponse)) {
	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	f(sm.deals[c.KeyString()].state)
}

func (sm *StorageMiner) processStorageDeal(c *cid.Cid) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := sm.getStorageDeal(c)
	if d.state.State != Accepted {
		// TODO: handle resumption of deal processing across miner restarts
		log.Error("attempted to process an already started deal")
		return
	}

	// 'Receive' the data, this could also be a truck full of hard drives. (TODO: proper abstraction)
	// TODO: this is not a great way to do this. At least use a session
	// Also, this needs to be fetched into a staging area for miners to prepare and seal in data
	if err := dag.FetchGraph(ctx, d.proposal.PieceRef, dag.NewDAGService(sm.nd.Blockservice)); err != nil {
		log.Errorf("failed to fetch data: %s", err)
		sm.updateDealState(c, func(resp *StorageDealResponse) {
			resp.Message = "Transfer failed"
			resp.State = Failed
			// TODO: signature?
		})
		return
	}

	fail := func(message, logerr string) {
		log.Errorf(logerr)
		sm.updateDealState(c, func(resp *StorageDealResponse) {
			resp.Message = message
			resp.State = Failed
		})
	}
	fmt.Println("adding piece", d.proposal.PieceRef)
	pi, err := sm.sectorBuilder().NewPieceInfo(d.proposal.PieceRef, d.proposal.Size.Uint64())
	if err != nil {
		fail("Failed to submit seal proof", fmt.Sprintf("failed to create piece info: %s", err))
		return
	}

	sectorID, err := sm.sectorBuilder().AddPiece(ctx, pi)
	if err != nil {
		fail("Failed to submit seal proof", fmt.Sprintf("failed to add piece: %s", err))
		return
	}

	fmt.Println("added piece to sector", sectorID)
	sm.dealsAwaitingSealLk.Lock()
	defer sm.dealsAwaitingSealLk.Unlock()
	deals, ok := sm.dealsAwaitingSeal[sectorID]
	if ok {
		sm.dealsAwaitingSeal[sectorID] = append(deals, c)
	} else {
		sm.dealsAwaitingSeal[sectorID] = []*cid.Cid{c}
	}

	sm.updateDealState(c, func(resp *StorageDealResponse) {
		resp.State = Staged
	})
}

// OnCommitmentAddedToMempool is a callback, called when a sector seal was commited to the chain.
func (sm *StorageMiner) OnCommitmentAddedToMempool(sector *SealedSector, msgCid *cid.Cid, err error) {
	sectorID := sector.GetID()
	fmt.Println("commitment added", sectorID)
	sm.dealsAwaitingSealLk.Lock()
	defer sm.dealsAwaitingSealLk.Unlock()
	deals, ok := sm.dealsAwaitingSeal[sectorID]
	if !ok {
		// nothing to do
		return
	}

	// remove the deals
	// TODO: reevaluate if this should be done inside the loops below
	sm.dealsAwaitingSeal[sectorID] = nil

	if err != nil {
		// we failed to seal this sector, cancel all the deals
		log.Errorf("failed sealing sector: %v: %s", sectorID, err)
		for _, c := range deals {
			go func(c *cid.Cid) {
				sm.updateDealState(c, func(resp *StorageDealResponse) {
					resp.Message = "Failed to seal sector"
					resp.State = Failed
				})
			}(c)
		}

		return
	}

	for _, c := range deals {
		go func(c *cid.Cid, sectorID uint64) {
			err = sm.nd.ChainMgr.WaitForMessage(
				context.Background(),
				msgCid,
				func(blk *types.Block, smgs *types.SignedMessage, receipt *types.MessageReceipt) error {
					if receipt.ExitCode != uint8(0) {
						return vmErrors.VMExitCodeToError(receipt.ExitCode, miner.Errors)
					}

					// Success, our seal is posted on chain
					sm.updateDealState(c, func(resp *StorageDealResponse) {
						resp.State = Posted
						//resp.ProofInfo = new(ProofInfo)
					})

					return nil
				},
			)
			if err != nil {
				log.Errorf("failed to commitSector: %s", err)
				sm.updateDealState(c, func(resp *StorageDealResponse) {
					resp.Message = "Failed to submit seal proof"
					resp.State = Failed
				})
				return
			}
		}(c, sectorID)
	}
}

// NewHeaviestTipSet is a callback called by node, everytime the the latest head is updated.
// It is used to check if we are in a new proving period and need to trigger PoSt submission.
func (sm *StorageMiner) NewHeaviestTipSet(ts core.TipSet) {
	sectors := sm.sectorBuilder().SealedSectors()
	if len(sectors) > 0 {
		fmt.Println("new heaviest tip set", sectors)
	}

	if len(sectors) == 0 {
		// no sector sealed, nothing to do
		return
	}

	provingPeriodStart, err := sm.getProvingPeriodStart()
	if err != nil {
		log.Errorf("failed to get provingPeriodStart: %s", err)
		return
	}

	sm.postInProcessLk.Lock()
	defer sm.postInProcessLk.Unlock()

	if sm.postInProcess == provingPeriodStart {
		// post is already being generated for this period, nothing to do
		return
	}

	height, err := ts.Height()
	if err != nil {
		log.Errorf("failed to get block height: %s", err)
		return
	}

	if types.NewBlockHeight(height).GreaterEqual(provingPeriodStart) {
		// we are in a new proving period, lets get this post going
		sm.postInProcess = provingPeriodStart
		go sm.submitPoSt()
	}
}

func (sm *StorageMiner) getProvingPeriodStart() (*types.BlockHeight, error) {
	res, code, err := sm.nd.CallQueryMethod(context.Background(), sm.minerAddr, "getProvingPeriodStart", []byte{}, nil)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("exitCode %d != 0", code)
	}

	return types.NewBlockHeightFromBytes(res[0]), nil
}

func (sm *StorageMiner) submitPoSt() {
	sectors := sm.sectorBuilder().SealedSectors()

	seeds := make([][]byte, len(sectors))
	sectorIDs := make([]uint64, len(sectors))
	for i, sector := range sectors {
		// TODO: real seed generation
		binary.LittleEndian.PutUint64(seeds[i], uint64(i))
		sectorIDs[i] = sector.GetID()
	}

	proof, faults, err := sm.sectorBuilder().GeneratePoSt(sectorIDs, seeds)
	if err != nil {
		log.Errorf("failed to generate PoSts: %s", err)
		return
	}
	if len(faults) != 0 {
		log.Errorf("some faults when generating PoSt: %v", faults)
		// TODO: proper fault handling
	}

	msgCid, err := sm.nd.SendMessage(context.TODO(), sm.minerOwnerAddr, sm.minerAddr, types.NewAttoFIL(nil), "submitPoSt", proof)
	if err != nil {
		log.Errorf("failed to submit PoSt: %s", err)
		return
	}

	err = sm.nd.ChainMgr.WaitForMessage(context.TODO(), msgCid, func(blk *types.Block, smgs *types.SignedMessage, receipt *types.MessageReceipt) error {
		if receipt.ExitCode != uint8(0) {
			return vmErrors.VMExitCodeToError(receipt.ExitCode, miner.Errors)
		}
		log.Infof("submitted PoSt")
		return nil
	})

	if err != nil {
		log.Errorf("failed to submit PoSt: %s", err)
	}
}

func (sm *StorageMiner) sectorBuilder() *SectorBuilder {
	return sm.nd.SectorBuilder
}

// Query responds to a query for the proposal referenced by the given cid
func (sm *StorageMiner) Query(ctx context.Context, c *cid.Cid) *StorageDealResponse {
	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	d, ok := sm.deals[c.KeyString()]
	if !ok {
		return &StorageDealResponse{
			State:   Unknown,
			Message: "no such deal",
		}
	}

	return d.state
}

type storageDealQueryRequest struct {
	Cid *cid.Cid
}

func (sm *StorageMiner) handleQuery(s inet.Stream) {
	defer s.Close() // nolint: errcheck

	var q storageDealQueryRequest
	if err := cbu.NewMsgReader(s).ReadMsg(&q); err != nil {
		panic(err)
	}

	ctx := context.Background()
	resp := sm.Query(ctx, q.Cid)

	if err := cbu.NewMsgWriter(s).WriteMsg(resp); err != nil {
		panic(err)
	}
}

// StorageMinerClient is a client interface to the StorageMiner
type StorageMinerClient struct {
	nd *Node

	deals   map[string]*clientStorageDealState
	dealsLk sync.Mutex
}

// NewStorageMinerClient creaters a new storage miner client
func NewStorageMinerClient(nd *Node) *StorageMinerClient {
	return &StorageMinerClient{
		nd:    nd,
		deals: make(map[string]*clientStorageDealState),
	}
}

type clientStorageDealState struct {
	miner     address.Address
	proposal  *StorageDealProposal
	lastState *StorageDealResponse
}

func getFileSize(ctx context.Context, c *cid.Cid, dserv ipld.DAGService) (uint64, error) {
	fnode, err := dserv.Get(ctx, c)
	if err != nil {
		return 0, err
	}
	switch n := fnode.(type) {
	case *dag.ProtoNode:
		return unixfs.DataSize(n.Data())
	case *dag.RawNode:
		return n.Size()
	default:
		return 0, fmt.Errorf("unrecognized node type: %T", fnode)
	}

}

// TryToStoreData needs a better name
func (smc *StorageMinerClient) TryToStoreData(ctx context.Context, miner address.Address, data *cid.Cid, duration uint64, price *types.AttoFIL) (*cid.Cid, error) {
	size, err := getFileSize(ctx, data, dag.NewDAGService(smc.nd.Blockservice))
	if err != nil {
		return nil, err
	}

	proposal := &StorageDealProposal{
		PieceRef:   data,
		Size:       types.NewBytesAmount(size),
		TotalPrice: price,
		Duration:   duration,
		//Payment:    PaymentInfo{},
		//Signature:  nil, // TODO: sign this
	}

	pid, err := smc.nd.Lookup.GetPeerIDByMinerAddress(ctx, miner)
	if err != nil {
		return nil, err
	}

	s, err := smc.nd.Host.NewStream(ctx, pid, StorageDealProtocolID)
	if err != nil {
		return nil, err
	}

	if err := cbu.NewMsgWriter(s).WriteMsg(proposal); err != nil {
		return nil, err
	}

	var response StorageDealResponse
	if err := cbu.NewMsgReader(s).ReadMsg(&response); err != nil {
		return nil, err
	}

	if err := smc.checkDealResponse(ctx, &response); err != nil {
		return nil, err
	}

	// TODO: send the miner the data (currently it gets requested by the miner, out of band)

	if err := smc.addResponseToTracker(&response, miner, proposal); err != nil {
		return nil, err
	}

	return response.Proposal, nil
}

func (smc *StorageMinerClient) addResponseToTracker(resp *StorageDealResponse, miner address.Address, p *StorageDealProposal) error {
	smc.dealsLk.Lock()
	defer smc.dealsLk.Unlock()
	k := resp.Proposal.KeyString()
	_, ok := smc.deals[k]
	if ok {
		return fmt.Errorf("deal in progress with that cid already exists")
	}

	smc.deals[k] = &clientStorageDealState{
		lastState: resp,
		miner:     miner,
		proposal:  p,
	}

	return nil
}

func (smc *StorageMinerClient) checkDealResponse(ctx context.Context, resp *StorageDealResponse) error {
	switch resp.State {
	case Rejected:
		return fmt.Errorf("deal rejected: %s", resp.Message)
	case Failed:
		return fmt.Errorf("deal failed: %s", resp.Message)
	default:
		return fmt.Errorf("invalid proposal response")
	case Accepted:
		return nil
	}
}

func (smc *StorageMinerClient) minerForProposal(c *cid.Cid) (address.Address, error) {
	smc.dealsLk.Lock()
	defer smc.dealsLk.Unlock()
	st, ok := smc.deals[c.KeyString()]
	if !ok {
		return address.Address{}, fmt.Errorf("no such proposal by cid: %s", c)
	}

	return st.miner, nil
}

// Query queries an in-progress proposal
func (smc *StorageMinerClient) Query(ctx context.Context, c *cid.Cid) (*StorageDealResponse, error) {
	mineraddr, err := smc.minerForProposal(c)
	if err != nil {
		return nil, err
	}

	minerpid, err := smc.nd.Lookup.GetPeerIDByMinerAddress(ctx, mineraddr)
	if err != nil {
		return nil, err
	}

	s, err := smc.nd.Host.NewStream(ctx, minerpid, StorageDealQueryProtocolID)
	if err != nil {
		return nil, err
	}

	q := storageDealQueryRequest{c}
	if err := cbu.NewMsgWriter(s).WriteMsg(q); err != nil {
		return nil, err
	}

	var resp StorageDealResponse
	if err := cbu.NewMsgReader(s).ReadMsg(&resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
