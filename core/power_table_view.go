package core

import (
	"context"

	"gx/ipfs/QmSKyB5faguXT4NqbrXpnRXqaVj5DhSm7x9BtzFydBY1UK/go-leb128"

	"github.com/filecoin-project/go-filecoin/actor/builtin/miner"
	"github.com/filecoin-project/go-filecoin/actor/builtin/storagemarket"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
)

// PowerTableView defines the set of functions used by the ChainManager to view
// the power table encoded in the tipset's state tree
type PowerTableView interface {
	// Total returns the total bytes stored by all miners in the given
	// state.
	Total(ctx context.Context, st state.Tree) (uint64, error)

	// Miner returns the total bytes stored by the miner of the
	// input address in the given state.
	Miner(ctx context.Context, st state.Tree, mAddr types.Address) (uint64, error)

	// HasPower returns true if the input address is associated with a
	// miner that has storage power in the network.
	HasPower(ctx context.Context, st state.Tree, mAddr types.Address) bool
}

type marketView struct{}

var _ PowerTableView = &marketView{}

// Total returns the total storage as a uint64.  If the total storage
// value exceeds the max value of a uint64 this method errors.
// TODO: uint64 has enough bits to express about 1 exabyte of total storage.
// This should be increased for v1.
func (v *marketView) Total(ctx context.Context, st state.Tree) (uint64, error) {
	var storage storagemarket.Storage
	err := st.GetActorStorage(ctx, address.StorageMarketAddress, &storage)
	if err != nil {
		return uint64(0), err
	}
	return leb128.ToUInt64(storage.TotalCommittedStorage.Bytes()), nil
}

// Miner returns the storage that this miner has committed as a uint64.
// If the total storage value exceeds the max value of a uint64 this method
// errors. TODO: uint64 has enough bits to express about 1 exabyte.  This
// should probably be increased for v1.
func (v *marketView) Miner(ctx context.Context, st state.Tree, mAddr types.Address) (uint64, error) {
	var mStorage miner.Storage
	err := st.GetActorStorage(ctx, mAddr, &mStorage)
	if err != nil {
		return uint64(0), err
	}
	return leb128.ToUInt64(mStorage.Power.Bytes()), nil
}

// HasPower returns true if the provided address belongs to a miner with power
// in the storage market
func (v *marketView) HasPower(ctx context.Context, st state.Tree, mAddr types.Address) bool {
	numBytes, err := v.Miner(ctx, st, mAddr)
	if err != nil || numBytes == uint64(0) {
		return false
	}
	return true
}
