package app

import (
	"context"
	"errors"
	"log"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"

	"sealstore/internal/tx"
)

type MyApp struct {
	// BaseApplication provides no-op defaults for mempool-connection ABCI
	// methods (InsertTx, ReapTxs) that this app doesn't customise.
	abcitypes.BaseApplication

	db           *badger.DB
	onGoingBlock *badger.Txn
}

func (m *MyApp) Info(ctx context.Context, info *abcitypes.RequestInfo) (*abcitypes.ResponseInfo, error) {
	//TODO implement me
	return &abcitypes.ResponseInfo{}, nil
}

func (m *MyApp) Query(ctx context.Context, query *abcitypes.RequestQuery) (*abcitypes.ResponseQuery, error) {
	//TODO implement me
	resp := abcitypes.ResponseQuery{Key: query.Data}

	dbErr := m.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(query.Data)
		if err != nil {
			if err != badger.ErrKeyNotFound {
				return err
			}
			resp.Log = "key does not exist"
			return nil
		}

		return item.Value(func(val []byte) error {
			resp.Log = "exists"
			resp.Value = val
			return nil
		})
	})

	if dbErr != nil {
		log.Panicf("Error reading databse, unable to execute query: %v", dbErr)
	}
	return &resp, nil
}

func (m *MyApp) CheckTx(ctx context.Context, tx *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	//TODO implement me
	code := m.isValid(tx.Tx)
	return &abcitypes.ResponseCheckTx{Code: code}, nil
}

func (m *MyApp) InitChain(ctx context.Context, chain *abcitypes.RequestInitChain) (*abcitypes.ResponseInitChain, error) {
	// Pre-credit the accounts named in the cometbft genesis app_state. Runs
	// once on a fresh chain; an absent app_state is a no-op.
	if err := m.applyGenesis(chain.AppStateBytes); err != nil {
		return nil, err
	}
	return &abcitypes.ResponseInitChain{}, nil
}

func (m *MyApp) PrepareProposal(ctx context.Context, proposal *abcitypes.RequestPrepareProposal) (*abcitypes.ResponsePrepareProposal, error) {
	//TODO implement me
	return &abcitypes.ResponsePrepareProposal{Txs: proposal.Txs}, nil
}

func (m *MyApp) ProcessProposal(ctx context.Context, proposal *abcitypes.RequestProcessProposal) (*abcitypes.ResponseProcessProposal, error) {
	//TODO implement me
	return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_ACCEPT}, nil
}

func (m *MyApp) FinalizeBlock(ctx context.Context, block *abcitypes.RequestFinalizeBlock) (*abcitypes.ResponseFinalizeBlock, error) {
	var tsx = make([]*abcitypes.ExecTxResult, len(block.Txs))

	m.onGoingBlock = m.db.NewTransaction(true)

	// Block clock (for commitment expiry): read the current height before any
	// tx runs so all expiry checks in this block agree on the same value.
	hcur, err := height(m.onGoingBlock)
	if err != nil {
		log.Printf("Error: reading height: %v", err)
		hcur = 0
	}

	for i, rawTx := range block.Txs {
		msg, perr := tx.Parse(rawTx)
		var err error
		if perr != nil {
			err = errors.New("unknown or malformed transaction")
		} else {
			switch mt := msg.(type) {
			case *tx.PayCommitTx:
				log.Printf("PayCommitTx: committing payment for account (%s)", mt.Acct)
				err = m.processPayCommit(m.onGoingBlock, mt)
			case *tx.PayRevealTx:
				log.Printf("PayRevealTx: revealing payment for account (%s)", mt.Body.From)
				err = m.processPayReveal(m.onGoingBlock, mt)
			default:
				err = errors.New("unknown or malformed transaction")
			}
		}
		if err != nil {
			log.Printf("Error: invalid transaction index %v: %v", i, err)
			tsx[i] = &abcitypes.ExecTxResult{Code: 1}
			continue
		}

		log.Printf("Accepted transaction index %v", i)
		tsx[i] = &abcitypes.ExecTxResult{Info: "hola hola!"}
	}

	// Advance the block clock so the next block's expiry checks see hcur+1.
	if err := bumpHeight(m.onGoingBlock, hcur); err != nil {
		log.Printf("Error: writing height: %v", err)
	}

	return &abcitypes.ResponseFinalizeBlock{TxResults: tsx}, nil
}

func (m *MyApp) ExtendVote(ctx context.Context, vote *abcitypes.RequestExtendVote) (*abcitypes.ResponseExtendVote, error) {
	//TODO implement me
	return &abcitypes.ResponseExtendVote{}, nil
}

func (m *MyApp) VerifyVoteExtension(ctx context.Context, extension *abcitypes.RequestVerifyVoteExtension) (*abcitypes.ResponseVerifyVoteExtension, error) {
	//TODO implement me
	return &abcitypes.ResponseVerifyVoteExtension{}, nil
}

func (m *MyApp) Commit(ctx context.Context, commit *abcitypes.RequestCommit) (*abcitypes.ResponseCommit, error) {
	//TODO implement me
	var err error
	if m.onGoingBlock != nil {
		err = m.onGoingBlock.Commit()
		m.onGoingBlock = nil
	}
	return &abcitypes.ResponseCommit{}, err
}

func (m *MyApp) ListSnapshots(ctx context.Context, snapshots *abcitypes.RequestListSnapshots) (*abcitypes.ResponseListSnapshots, error) {
	//TODO implement me
	return &abcitypes.ResponseListSnapshots{}, nil
}

func (m *MyApp) OfferSnapshot(ctx context.Context, snapshot *abcitypes.RequestOfferSnapshot) (*abcitypes.ResponseOfferSnapshot, error) {
	//TODO implement me
	return &abcitypes.ResponseOfferSnapshot{}, nil
}

func (m *MyApp) LoadSnapshotChunk(ctx context.Context, chunk *abcitypes.RequestLoadSnapshotChunk) (*abcitypes.ResponseLoadSnapshotChunk, error) {
	//TODO implement me
	return &abcitypes.ResponseLoadSnapshotChunk{}, nil
}

func (m *MyApp) ApplySnapshotChunk(ctx context.Context, chunk *abcitypes.RequestApplySnapshotChunk) (*abcitypes.ResponseApplySnapshotChunk, error) {
	return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ACCEPT}, nil
}

var _ abcitypes.Application = (*MyApp)(nil)

func SealstoreAbciApp(db *badger.DB) *MyApp {
	return &MyApp{
		db:           db,
		onGoingBlock: nil,
	}
}

func (app *MyApp) isValid(rawTx []byte) uint32 {
	if _, err := tx.Parse(rawTx); err != nil {
		return 1
	}
	return 0
}
