package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"

	"sealstore/tx"
)

// commitKey namespaces commitment entries so they never collide with final values.
func commitKey(key []byte) []byte {
	return append([]byte("commit/"), key...)
}

// hash returns the sha256 digest of b, used as the commitment payload.
func hash(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

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
	//TODO implement me
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
	//TODO implement me
	var tsx = make([]*abcitypes.ExecTxResult, len(block.Txs))

	m.onGoingBlock = m.db.NewTransaction(true)
	for i, rawTx := range block.Txs {
		msg, perr := tx.Parse(rawTx)
		var key []byte
		var err error
		if perr != nil {
			err = errors.New("unknown or malformed transaction")
		} else {
			switch mt := msg.(type) {
			case *tx.CommitTx:
				key = mt.Key
				log.Printf("CommitTx: committing hash for key (%s)", key)
				err = m.onGoingBlock.Set(commitKey(mt.Key), mt.Hash[:])
			case *tx.RevealTx:
				key = mt.Key
				log.Printf("RevealTx: revealing value for key (%s)", key)
				err = m.processReveal(m.onGoingBlock, mt.Key, mt.Value)
			default:
				err = errors.New("unknown or malformed transaction")
			}
		}
		if err != nil {
			log.Printf("Error: invalid transaction index %v: %v", i, err)
			tsx[i] = &abcitypes.ExecTxResult{Code: 1}
			continue
		}

		log.Printf("Successivly added key (%s)", key)
		tsx[i] = &abcitypes.ExecTxResult{Info: "hola hola!"}
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

func NewMyApp(db *badger.DB) *MyApp {
	return &MyApp{
		db:           db,
		onGoingBlock: nil,
	}
}

func (app *MyApp) isValid(rawTx []byte) uint32 {
	msg, err := tx.Parse(rawTx)
	if err != nil {
		return 1
	}
	// A reveal of an empty value carries no information; reject it at the
	// mempool just like the old string format did. A commit always carries a
	// 32-byte hash, so it is never rejected for an empty payload.
	if r, ok := msg.(*tx.RevealTx); ok && len(r.Value) == 0 {
		return 1
	}
	return 0
}

// processReveal verifies a reveal against the stored commitment and, on match,
// stores the final value. The commitment is deliberately kept (not consumed) so
// it stays verifiable afterward: anyone can re-check that hash(value) equals the
// published commitment.
func (m *MyApp) processReveal(txn *badger.Txn, key, value []byte) error {
	ckey := commitKey(key)
	var stored []byte
	item, err := txn.Get(ckey)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return errors.New("reveal without a prior commit")
		}
		return err
	}
	if err := item.Value(func(val []byte) error {
		stored = append([]byte(nil), val...)
		return nil
	}); err != nil {
		return err
	}

	if !bytes.Equal(hash(value), stored) {
		return errors.New("reveal does not match the committed hash")
	}

	return txn.Set(key, value)
}
