package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"
)

// TxType distinguishes the two transaction kinds handled by MyApp.
type TxType uint8

const (
	TxUnknown TxType = iota
	TxCommit
	TxReveal
)

var (
	commitPrefix = []byte("commit=")
	revealPrefix = []byte("reveal=")
)

// parseTx classifies a raw transaction and extracts its key and payload.
// CommitTx wire format:  commit=key=hash
// RevealTx wire format:  reveal=key=value
// An empty, wrongly-prefixed, or payload-less tx yields TxUnknown.
func parseTx(tx []byte) (typ TxType, key, payload []byte) {
	for _, p := range []struct {
		prefix []byte
		typ    TxType
	}{
		{commitPrefix, TxCommit},
		{revealPrefix, TxReveal},
	} {
		if !bytes.HasPrefix(tx, p.prefix) {
			continue
		}
		parts := bytes.SplitN(tx[len(p.prefix):], []byte("="), 2)
		if len(parts) != 2 {
			return TxUnknown, nil, nil
		}
		return p.typ, parts[0], parts[1]
	}
	return TxUnknown, nil, nil
}

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
		typ, key, payload := parseTx(tx)
		var err error
		switch typ {
		case TxCommit:
			log.Printf("CommitTx: committing hash for key (%s)", key)
			err = m.onGoingBlock.Set(commitKey(key), payload)
		case TxReveal:
			log.Printf("RevealTx: revealing value for key (%s)", key)
			err = m.processReveal(m.onGoingBlock, key, payload)
		default:
			err = errors.New("unknown or malformed transaction")
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
	return &MyApp{db, nil}
}

func (app *MyApp) isValid(tx []byte) uint32 {
	typ, _, payload := parseTx(tx)
	if typ == TxUnknown || len(payload) == 0 {
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
