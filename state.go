package main

import (
	"bytes"
	"context"
	"log"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"
)

type MyApp struct {
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
	for i, tx := range block.Txs {
		if code := m.isValid(tx); code != 0 {
			log.Printf("Error: invalid transaction index %v", i)
			tsx[i] = &abcitypes.ExecTxResult{Code: code}
		} else {
			parts := bytes.SplitN(tx, []byte("="), 2)
			key, value := parts[0], parts[1]
			log.Printf("Adding key (%s) with value (%s)", key, value)

			if err := m.onGoingBlock.Set(key, value); err != nil {
				log.Panicf("Error writing to database, unable to execute tx: %v", err)
			}

			log.Printf("Successivly added key (%s) with val (%s) do databse", key, value)
			tsx[i] = &abcitypes.ExecTxResult{Info: "hola hola!"}
		}
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
	return &MyApp{db, nil}
}

func (app *MyApp) isValid(tx []byte) uint32 {
	parts := bytes.Split(tx, []byte("="))
	if len(parts) != 2 {
		return 1
	}
	return 0
}
