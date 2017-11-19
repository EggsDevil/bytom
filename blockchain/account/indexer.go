package account

import (
	"context"
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"github.com/tendermint/tmlibs/db"

	"github.com/bytom/blockchain/query"
	"github.com/bytom/blockchain/signers"
	"github.com/bytom/crypto/sha3pool"
	chainjson "github.com/bytom/encoding/json"
	"github.com/bytom/errors"
	"github.com/bytom/protocol"
	"github.com/bytom/protocol/bc"
	"github.com/bytom/protocol/bc/legacy"
)

const (
	//UTXOPreFix is account unspent outputs store db by key with this prefix
	UTXOPreFix = "ACU:"
)

var walletkey = []byte("walletInfo")

func accountUTXOKey(name string) []byte {
	return []byte(UTXOPreFix + name)
}

//WalletInfo is base valid block info to handle orphan block rollback
type WalletInfo struct {
	Height uint64
	Hash   bc.Hash
}

//Wallet is related to storing account unspent outputs
type Wallet struct {
	DB db.DB
	WalletInfo
}

//NewWallet return a new wallet instance
func NewWallet(db db.DB) *Wallet {
	w := &Wallet{
		DB: db,
	}

	walletInfo, err := w.GetWalletInfo()
	if err != nil {
		log.WithField("warn", err).Warn("get wallet info")
	}
	w.Height = walletInfo.Height
	w.Hash = walletInfo.Hash
	return w
}

//GetWalletHeight return wallet on current height
func (w *Wallet) GetWalletHeight() uint64 {
	return w.Height
}

//GetWalletInfo return stored wallet info and nil,if error,
//return initial wallet info and err
func (w *Wallet) GetWalletInfo() (WalletInfo, error) {
	var info WalletInfo
	var rawWallet []byte

	if rawWallet = w.DB.Get(walletkey); rawWallet == nil {
		return info, nil
	}

	if err := json.Unmarshal(rawWallet, &w); err != nil {
		return info, err
	}

	return info, nil

}

//WalletUpdate process every valid block and reverse every invalid block which need to rollback
func (m *Manager) WalletUpdate(c *protocol.Chain) {
	var err error
	var block *legacy.Block

	storeBatch := m.wallet.DB.NewBatch()

LOOP:

	for !c.InMainChain(m.wallet.Height, m.wallet.Hash) {
		if block, err = c.GetBlockByHash(&m.wallet.Hash); err != nil {
			log.WithField("err", err).Error("get block by hash")
			return
		}

		//Reverse this block
		m.ReverseAccountUTXOs(&storeBatch, block)
		log.WithField("Height", m.wallet.Height).Info("start rollback this block")

		m.wallet.Height = block.Height - 1
		m.wallet.Hash = block.PreviousBlockHash

	}

	//update wallet info and commit batch write
	m.wallet.commitWalletInfo(&storeBatch)

	block, _ = c.GetBlockByHeight(m.wallet.Height + 1)
	//if we already handled the tail of the chain, we wait
	if block == nil {
		<-c.BlockWaiter(m.wallet.Height + 1)
		if block, err = c.GetBlockByHeight(m.wallet.Height + 1); err != nil {
			log.WithField("err", err).Error("wallet get block by height")
			return
		}
	}

	//if false, means that rollback operation is necessary,then goto LOOP
	if block.PreviousBlockHash == m.wallet.Hash {
		//next loop will save
		m.wallet.Height = block.Height
		m.wallet.Hash = block.Hash()
		m.BuildAccountUTXOs(&storeBatch, block)

		//update wallet info and commit batch write
		m.wallet.commitWalletInfo(&storeBatch)
	}

	//goto next loop
	goto LOOP

}

func (w *Wallet) commitWalletInfo(batch *db.Batch) {
	var info WalletInfo

	info.Height = w.Height
	info.Hash = w.Hash

	rawWallet, err := json.Marshal(info)
	if err != nil {
		log.WithField("err", err).Error("save wallet info")
		return
	}
	//update wallet to db
	(*batch).Set(walletkey, rawWallet)
	//commit to db
	(*batch).Write()
}

//UTXO is a structure about account unspent outputs
type UTXO struct {
	OutputID     []byte
	AssetID      []byte
	Amount       uint64
	AccountID    string
	ProgramIndex uint64
	Program      []byte
	SourceID     []byte
	SourcePos    uint64
	RefData      []byte
	Change       bool
}

var emptyJSONObject = json.RawMessage(`{}`)

// A Saver is responsible for saving an annotated account object.
// for indexing and retrieval.
// If the Core is configured not to provide search services,
// SaveAnnotatedAccount can be a no-op.
type Saver interface {
	SaveAnnotatedAccount(context.Context, *query.AnnotatedAccount) error
}

//Annotated init an annotated account object
func Annotated(a *Account) (*query.AnnotatedAccount, error) {
	aa := &query.AnnotatedAccount{
		ID:     a.ID,
		Alias:  a.Alias,
		Quorum: a.Quorum,
		Tags:   &emptyJSONObject,
	}

	tags, err := json.Marshal(a.Tags)
	if err != nil {
		return nil, err
	}
	if len(tags) > 0 {
		rawTags := json.RawMessage(tags)
		aa.Tags = &rawTags
	}

	path := signers.Path(a.Signer, signers.AccountKeySpace)
	var jsonPath []chainjson.HexBytes
	for _, p := range path {
		jsonPath = append(jsonPath, p)
	}
	for _, xpub := range a.XPubs {
		aa.Keys = append(aa.Keys, &query.AccountKey{
			RootXPub:              xpub,
			AccountXPub:           xpub.Derive(path),
			AccountDerivationPath: jsonPath,
		})
	}
	return aa, nil
}

func (m *Manager) indexAnnotatedAccount(ctx context.Context, a *Account) error {
	if m.indexer == nil {
		return nil
	}
	aa, err := Annotated(a)
	if err != nil {
		return err
	}
	return m.indexer.SaveAnnotatedAccount(ctx, aa)
}

type rawOutput struct {
	OutputID bc.Hash
	bc.AssetAmount
	ControlProgram []byte
	txHash         bc.Hash
	outputIndex    uint32
	sourceID       bc.Hash
	sourcePos      uint64
	refData        bc.Hash
}

type accountOutput struct {
	rawOutput
	AccountID string
	keyIndex  uint64
	change    bool
}

//ReverseAccountUTXOs process the invalid blocks when orphan block rollback
func (m *Manager) ReverseAccountUTXOs(batch *db.Batch, b *legacy.Block) {
	var err error

	//unknow how many spent and retire outputs
	reverseOuts := make([]*rawOutput, 0)

	//handle spent UTXOs
	for _, tx := range b.Transactions {
		for _, inpID := range tx.Tx.InputIDs {
			//spend and retire
			sp, err := tx.Spend(inpID)
			if err != nil {
				continue
			}

			resOut, ok := tx.Entries[*sp.SpentOutputId].(*bc.Output)
			if !ok {
				continue
			}

			out := &rawOutput{
				OutputID:       *sp.SpentOutputId,
				AssetAmount:    *resOut.Source.Value,
				ControlProgram: resOut.ControlProgram.Code,
				txHash:         tx.ID,
				sourceID:       *resOut.Source.Ref,
				sourcePos:      resOut.Source.Position,
				refData:        *resOut.Data,
			}
			reverseOuts = append(reverseOuts, out)
		}
	}

	accOuts := m.loadAccountInfo(reverseOuts)
	if err = m.upsertConfirmedAccountOutputs(accOuts, b, batch); err != nil {
		log.WithField("err", err).Error("reversing account spent and retire outputs")
		return
	}

	//handle new UTXOs
	for _, tx := range b.Transactions {
		for j := range tx.Outputs {
			resOutID := tx.ResultIds[j]
			if _, ok := tx.Entries[*resOutID].(*bc.Output); !ok {
				//retirement
				continue
			}
			//delete new UTXOs
			(*batch).Delete(accountUTXOKey(string(resOutID.Bytes())))
		}
	}

}

//BuildAccountUTXOs process valid blocks to build account unspent outputs db
func (m *Manager) BuildAccountUTXOs(batch *db.Batch, b *legacy.Block) {
	var err error

	//handle spent UTXOs
	delOutputIDs := prevoutDBKeys(b.Transactions...)
	for _, delOutputID := range delOutputIDs {
		(*batch).Delete(accountUTXOKey(string(delOutputID.Bytes())))
	}

	//handle new UTXOs
	outs := make([]*rawOutput, 0, len(b.Transactions))
	for _, tx := range b.Transactions {
		for j, out := range tx.Outputs {
			resOutID := tx.ResultIds[j]
			resOut, ok := tx.Entries[*resOutID].(*bc.Output)
			if !ok {
				continue
			}
			out := &rawOutput{
				OutputID:       *tx.OutputID(j),
				AssetAmount:    out.AssetAmount,
				ControlProgram: out.ControlProgram,
				txHash:         tx.ID,
				outputIndex:    uint32(j),
				sourceID:       *resOut.Source.Ref,
				sourcePos:      resOut.Source.Position,
				refData:        *resOut.Data,
			}
			outs = append(outs, out)
		}
	}
	accOuts := m.loadAccountInfo(outs)

	if err = m.upsertConfirmedAccountOutputs(accOuts, b, batch); err != nil {
		log.WithField("err", err).Error("building new account outputs")
		return
	}
}

func prevoutDBKeys(txs ...*legacy.Tx) (outputIDs []bc.Hash) {
	for _, tx := range txs {
		for _, inpID := range tx.Tx.InputIDs {
			if sp, err := tx.Spend(inpID); err == nil {
				outputIDs = append(outputIDs, *sp.SpentOutputId)
			}
		}
	}
	return
}

// loadAccountInfo turns a set of output IDs into a set of
// outputs by adding account annotations.  Outputs that can't be
// annotated are excluded from the result.
func (m *Manager) loadAccountInfo(outs []*rawOutput) []*accountOutput {
	outsByScript := make(map[string][]*rawOutput, len(outs))
	for _, out := range outs {
		scriptStr := string(out.ControlProgram)
		outsByScript[scriptStr] = append(outsByScript[scriptStr], out)
	}

	result := make([]*accountOutput, 0, len(outs))
	cp := controlProgram{}

	var hash [32]byte
	for s := range outsByScript {
		sha3pool.Sum256(hash[:], []byte(s))
		bytes := m.db.Get(accountCPKey(hash))
		if bytes == nil {
			continue
		}

		err := json.Unmarshal(bytes, &cp)
		if err != nil {
			continue
		}

		//filte the accounts which exists in accountdb with wallet enabled
		//TODO:filte receiver UTXO about self ?
		isExist := m.db.Get(accountKey(cp.AccountID))
		if isExist == nil {
			continue
		}

		for _, out := range outsByScript[s] {
			newOut := &accountOutput{
				rawOutput: *out,
				AccountID: cp.AccountID,
				keyIndex:  cp.KeyIndex,
				change:    cp.Change,
			}
			result = append(result, newOut)
		}
	}

	return result
}

// upsertConfirmedAccountOutputs records the account data for confirmed utxos.
// If the account utxo already exists (because it's from a local tx), the
// block confirmation data will in the row will be updated.
func (m *Manager) upsertConfirmedAccountOutputs(outs []*accountOutput, block *legacy.Block, batch *db.Batch) error {
	var u *UTXO

	for _, out := range outs {
		u = &UTXO{OutputID: out.OutputID.Bytes(),
			AssetID:      out.AssetId.Bytes(),
			Amount:       out.Amount,
			AccountID:    out.AccountID,
			ProgramIndex: out.keyIndex,
			Program:      out.ControlProgram,
			SourceID:     out.sourceID.Bytes(),
			SourcePos:    out.sourcePos,
			RefData:      out.refData.Bytes(),
			Change:       out.change}

		rawUTXO, err := json.Marshal(u)
		if err != nil {
			return errors.Wrap(err, "failed marshal accountutxo")
		}

		(*batch).Set(accountUTXOKey(string(u.OutputID)), rawUTXO)
	}
	return nil
}
