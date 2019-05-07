package chain

import (
	"context"

	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/metrics/tracing"
	"github.com/filecoin-project/go-filecoin/sampling"
	"github.com/filecoin-project/go-filecoin/types"
)

var errIterComplete = errors.New("unexpected complete iterator")

// GetRecentAncestorsOfHeaviestChain returns the ancestors of a `TipSet` with
// height `descendantBlockHeight` in the heaviest chain.
func GetRecentAncestorsOfHeaviestChain(ctx context.Context, chainReader ReadStore, descendantBlockHeight *types.BlockHeight) ([]types.TipSet, error) {
	head := chainReader.GetHead()
	headTipSet, err := chainReader.GetTipSet(head)
	if err != nil {
		return nil, err
	}
	ancestorHeight := types.NewBlockHeight(consensus.AncestorRoundsNeeded)
	return GetRecentAncestors(ctx, *headTipSet, chainReader, descendantBlockHeight, ancestorHeight, sampling.LookbackParameter)
}

// GetRecentAncestors returns the ancestors of base as a slice of TipSets.
//
// In order to validate post messages, randomness from the chain is required.
// This function collects that randomess: all tipsets with height greater than
// childBH - ancestorRounds, and the lookback tipsets that precede them.
//
// The return slice is a concatenation of two slices: append(provingPeriodAncestors, extraRandomnessAncestors...)
//   provingPeriodAncestors: all ancestor tipsets with height greater than childBH - ancestorRoundsNeeded
//   extraRandomnessAncestors: the lookback number of tipsets directly preceding tipsets in provingPeriodAncestors
//
// The last tipset of provingPeriodAncestors is the earliest possible tipset to
// begin a proving period that is still "live", i.e it is valid to accept PoSts
// over this proving period when processing a tipset at childBH.  The last
// tipset of extraRandomnessAncestors is the tipset used to sample randomness
// for any PoSts with a proving period beginning at the last tipset of
// provingPeriodAncestors.  By including ancestors as far back as the last tipset
// of extraRandomnessAncestors, the consensus state transition function can sample
// the randomness used by all live PoSts to correctly process all valid
// 'submitPoSt' messages.
//
// Because null blocks increase chain height but do not have associated tipsets
// the length of provingPeriodAncestors may vary (more null blocks -> shorter length).  The
// length of slice extraRandomnessAncestors is a constant (at least once the
// chain is longer than lookback tipsets).
func GetRecentAncestors(ctx context.Context, base types.TipSet, chainReader ReadStore, childBH, ancestorRoundsNeeded *types.BlockHeight, lookback uint) (ts []types.TipSet, err error) {
	ctx, span := trace.StartSpan(ctx, "Chain.GetRecentAncestors")
	defer tracing.AddErrorEndSpan(ctx, span, &err)

	if lookback == 0 {
		return nil, errors.New("lookback must be greater than 0")
	}
	earliestAncestorHeight := childBH.Sub(ancestorRoundsNeeded)
	if earliestAncestorHeight.LessThan(types.NewBlockHeight(0)) {
		earliestAncestorHeight = types.NewBlockHeight(uint64(0))
	}

	// Step 1 -- gather all tipsets with a height greater than the earliest
	// possible proving period start still in scope for the given head.
	iterator := IterAncestors(ctx, chainReader, base)
	provingPeriodAncestors, err := CollectTipSetsOfHeightAtLeast(ctx, iterator, earliestAncestorHeight)
	if err != nil {
		return nil, err
	}
	firstExtraRandomnessAncestorsCids, err := provingPeriodAncestors[len(provingPeriodAncestors)-1].Parents()
	if err != nil {
		return nil, err
	}
	// no parents means hit genesis so return the whole chain
	if firstExtraRandomnessAncestorsCids.Len() == 0 {
		return provingPeriodAncestors, nil
	}

	// Step 2 -- gather the lookback tipsets directly preceding provingPeriodAncestors.
	lookBackTS, err := chainReader.GetTipSet(firstExtraRandomnessAncestorsCids)
	if err != nil {
		return nil, err
	}
	iterator = IterAncestors(ctx, chainReader, *lookBackTS)
	extraRandomnessAncestors, err := CollectAtMostNTipSets(ctx, iterator, lookback)
	if err != nil {
		return nil, err
	}
	return append(provingPeriodAncestors, extraRandomnessAncestors...), nil
}

// CollectTipSetsOfHeightAtLeast collects all tipsets with a height greater
// than or equal to minHeight from the input tipset.
func CollectTipSetsOfHeightAtLeast(ctx context.Context, iterator *TipsetIterator, minHeight *types.BlockHeight) ([]types.TipSet, error) {
	var ret []types.TipSet
	var err error
	var h uint64
	for ; !iterator.Complete(); err = iterator.Next() {
		if err != nil {
			return nil, err
		}
		h, err = iterator.Value().Height()
		if err != nil {
			return nil, err
		}
		if types.NewBlockHeight(h).LessThan(minHeight) {
			return ret, nil
		}
		ret = append(ret, iterator.Value())
	}
	return ret, nil
}

// CollectAtMostNTipSets collect N tipsets from the input channel.  If there
// are fewer than n tipsets in the channel it returns all of them.
func CollectAtMostNTipSets(ctx context.Context, iterator *TipsetIterator, n uint) ([]types.TipSet, error) {
	var ret []types.TipSet
	var err error
	for i := uint(0); i < n && !iterator.Complete(); i++ {
		ret = append(ret, iterator.Value())
		if err = iterator.Next(); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

// FindCommonAncestor returns the common ancestor of the two tipsets pointed to
// by the input iterators.  If they share no common ancestor errIterComplete
// will be returned.
func FindCommonAncestor(oldIter, newIter *TipsetIterator) (types.TipSet, error) {
	for {
		old := oldIter.Value()
		new := newIter.Value()

		oldHeight, err := old.Height()
		if err != nil {
			return nil, err
		}
		newHeight, err := new.Height()
		if err != nil {
			return nil, err
		}

		// Found common ancestor.
		if old.Equals(new) {
			return old, nil
		}

		// Update one pointer. Each iteration will move the pointer at
		// a higher chain height to the other pointer's height, or, if
		// that height is a null block in the moving pointer's chain,
		// it will move this pointer to the first available height lower
		// than the other pointer.
		if oldHeight < newHeight {
			if err := iterToHeightOrLower(newIter, oldHeight); err != nil {
				return nil, err
			}
		} else if newHeight < oldHeight {
			if err := iterToHeightOrLower(oldIter, newHeight); err != nil {
				return nil, err
			}
		} else { // move old down one when oldHeight == newHeight
			if err := iterToHeightOrLower(oldIter, oldHeight-uint64(1)); err != nil {
				return nil, err
			}
			if err := iterToHeightOrLower(newIter, newHeight-uint64(1)); err != nil {
				return nil, err
			}
		}
	}
}

// iterToHeightOrLower moves the provided tipset iterator back in the chain
// until the iterator points to the first tipset in the chain with a height
// less than or equal to endHeight.  If the iterator is complete before
// reaching this height errIterComplete is returned.
func iterToHeightOrLower(iter *TipsetIterator, endHeight uint64) error {
	for {
		if iter.Complete() {
			return errIterComplete
		}
		ts := iter.Value()
		height, err := ts.Height()
		if err != nil {
			return err
		}
		if height <= endHeight {
			return nil
		}
		if err := iter.Next(); err != nil {
			return err
		}

	}
}
