package vmutil

import (
	"github.com/bytom/crypto/ed25519"
	"github.com/bytom/errors"
	"github.com/bytom/protocol/vm"
)

// pre-define errors
var (
	ErrBadValue       = errors.New("bad value")
	ErrMultisigFormat = errors.New("bad multisig program format")
)

// IsUnspendable checks if a contorl program is absolute failed
func IsUnspendable(prog []byte) bool {
	return len(prog) > 0 && prog[0] == byte(vm.OP_FAIL)
}

func (b *Builder) addP2SPMultiSig(pubkeys []ed25519.PublicKey, nrequired int) error {
	if err := checkMultiSigParams(int64(nrequired), int64(len(pubkeys))); err != nil {
		return err
	}

	b.AddOp(vm.OP_DUP).AddOp(vm.OP_TOALTSTACK) // stash a copy of the predicate
	b.AddOp(vm.OP_SHA3)                        // stack is now [... NARGS SIG SIG SIG PREDICATEHASH]
	for _, p := range pubkeys {
		b.AddData(p)
	}
	b.AddInt64(int64(nrequired))                     // stack is now [... SIG SIG SIG PREDICATEHASH PUB PUB PUB M]
	b.AddInt64(int64(len(pubkeys)))                  // stack is now [... SIG SIG SIG PREDICATEHASH PUB PUB PUB M N]
	b.AddOp(vm.OP_CHECKMULTISIG).AddOp(vm.OP_VERIFY) // stack is now [... NARGS]
	b.AddOp(vm.OP_FROMALTSTACK)                      // stack is now [... NARGS PREDICATE]
	b.AddInt64(0).AddOp(vm.OP_CHECKPREDICATE)
	return nil
}

// CoinbaseProgram generates the script for contorl coinbase output
func CoinbaseProgram(pubkeys []ed25519.PublicKey, nrequired int, height uint64) ([]byte, error) {
	builder := NewBuilder()
	builder.AddOp(vm.OP_BLOCKHEIGHT)
	builder.AddInt64(int64(height))
	builder.AddOp(vm.OP_GREATERTHAN)
	builder.AddOp(vm.OP_VERIFY)

	if nrequired == 0 {
		return builder.Build()
	}
	if err := builder.addP2SPMultiSig(pubkeys, nrequired); err != nil {
		return nil, err
	}
	return builder.Build()
}

// P2SPMultiSigProgram generates the script for contorl transaction output
func P2SPMultiSigProgram(pubkeys []ed25519.PublicKey, nrequired int) ([]byte, error) {
	builder := NewBuilder()
	if err := builder.addP2SPMultiSig(pubkeys, nrequired); err != nil {
		return nil, err
	}
	return builder.Build()
}

// ParseP2SPMultiSigProgram is unknow for us yet
func ParseP2SPMultiSigProgram(program []byte) ([]ed25519.PublicKey, int, error) {
	pops, err := vm.ParseProgram(program)
	if err != nil {
		return nil, 0, err
	}
	if len(pops) < 11 {
		return nil, 0, vm.ErrShortProgram
	}

	// Count all instructions backwards from the end in case there are
	// extra instructions at the beginning of the program (like a
	// <pushdata> DROP).

	npubkeys, err := vm.AsInt64(pops[len(pops)-6].Data)
	if err != nil {
		return nil, 0, err
	}
	if int(npubkeys) > len(pops)-10 {
		return nil, 0, vm.ErrShortProgram
	}
	nrequired, err := vm.AsInt64(pops[len(pops)-7].Data)
	if err != nil {
		return nil, 0, err
	}
	err = checkMultiSigParams(nrequired, npubkeys)
	if err != nil {
		return nil, 0, err
	}

	firstPubkeyIndex := len(pops) - 7 - int(npubkeys)

	pubkeys := make([]ed25519.PublicKey, 0, npubkeys)
	for i := firstPubkeyIndex; i < firstPubkeyIndex+int(npubkeys); i++ {
		if len(pops[i].Data) != ed25519.PublicKeySize {
			return nil, 0, err
		}
		pubkeys = append(pubkeys, ed25519.PublicKey(pops[i].Data))
	}
	return pubkeys, int(nrequired), nil
}

func checkMultiSigParams(nrequired, npubkeys int64) error {
	if nrequired < 0 {
		return errors.WithDetail(ErrBadValue, "negative quorum")
	}
	if npubkeys < 0 {
		return errors.WithDetail(ErrBadValue, "negative pubkey count")
	}
	if nrequired > npubkeys {
		return errors.WithDetail(ErrBadValue, "quorum too big")
	}
	if nrequired == 0 && npubkeys > 0 {
		return errors.WithDetail(ErrBadValue, "quorum empty with non-empty pubkey list")
	}
	return nil
}
