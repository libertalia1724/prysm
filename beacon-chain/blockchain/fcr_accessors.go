package blockchain

import (
	"context"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/confirmation"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/helpers"
	coreTime "github.com/OffchainLabs/prysm/v7/beacon-chain/core/time"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/transition"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/state"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/time/slots"
	"github.com/pkg/errors"
)

type fcrCommitteeAccessor struct {
	s *Service
}

func (a *fcrCommitteeAccessor) Committee(ctx context.Context, slot primitives.Slot) ([]primitives.ValidatorIndex, error) {
	a.s.headLock.RLock()
	headState := a.s.head.state
	a.s.headLock.RUnlock()
	if headState == nil || headState.IsNil() {
		return nil, errors.New("head state not available")
	}
	committees, err := helpers.BeaconCommittees(ctx, headState, slot)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, committee := range committees {
		n += len(committee)
	}
	result := make([]primitives.ValidatorIndex, 0, n)
	for _, committee := range committees {
		result = append(result, committee...)
	}
	return result, nil
}

type balanceInfo struct {
	balances []uint64
	total    uint64
}

type fcrBalanceAccessor struct {
	s *Service
	// Checkpoint states are immutable, cached entries never go stale.
	byRoot map[[32]byte]balanceInfo
}

func extractBalanceInfo(ctx context.Context, st state.ReadOnlyBeaconState) ([]uint64, uint64, error) {
	total, err := helpers.TotalActiveBalance(ctx, st)
	if err != nil {
		return nil, 0, err
	}
	balances := make([]uint64, st.NumValidators())
	epoch := coreTime.CurrentEpoch(st)
	for idx, val := range st.ValidatorsReadOnlySeq() {
		if helpers.IsActiveValidatorUsingTrie(val, epoch) && !val.Slashed() {
			balances[idx] = val.EffectiveBalance()
		}
	}
	return balances, total, nil
}

func (a *fcrBalanceAccessor) BalanceInfoByCheckpoint(ctx context.Context, root [32]byte) ([]uint64, uint64, error) {
	if cached, ok := a.byRoot[root]; ok {
		return cached.balances, cached.total, nil
	}
	st, err := a.s.cfg.StateGen.StateByRoot(ctx, root)
	if err != nil {
		return nil, 0, errors.Wrap(err, "could not get state for checkpoint root")
	}
	if st == nil || st.IsNil() {
		return nil, 0, errors.New("nil state for checkpoint root")
	}
	balances, total, err := extractBalanceInfo(ctx, st)
	if err != nil {
		return nil, 0, err
	}
	if a.byRoot == nil {
		a.byRoot = make(map[[32]byte]balanceInfo)
	} else if len(a.byRoot) > 3 {
		for k := range a.byRoot {
			delete(a.byRoot, k)
			break
		}
	}
	a.byRoot[root] = balanceInfo{balances: balances, total: total}
	return balances, total, nil
}

func (a *fcrBalanceAccessor) PulledUpHeadState(ctx context.Context) (*confirmation.FFGStateInfo, error) {
	a.s.headLock.RLock()
	headState := a.s.head.state
	headRoot := a.s.headRoot()
	a.s.headLock.RUnlock()

	if headState == nil || headState.IsNil() {
		return nil, errors.New("head state not available")
	}

	currentEpoch := slots.EpochsSinceGenesis(a.s.genesisTime)
	stateEpoch := coreTime.CurrentEpoch(headState)

	var st state.ReadOnlyBeaconState
	if stateEpoch < currentEpoch {
		epochStart, err := slots.EpochStart(currentEpoch)
		if err != nil {
			return nil, err
		}
		// Prefer the nextSlotCache to avoid a state copy plus ProcessSlots.
		cached := transition.NextSlotState(headRoot[:], epochStart)
		if cached != nil && !cached.IsNil() {
			if cached.Slot() < epochStart {
				advanced, err := transition.ProcessSlots(ctx, cached, epochStart)
				if err != nil {
					return nil, errors.Wrap(err, "could not advance cached state")
				}
				st = advanced
			} else {
				st = cached
			}
		} else {
			copied := headState.Copy()
			advanced, err := transition.ProcessSlots(ctx, copied, epochStart)
			if err != nil {
				return nil, errors.Wrap(err, "could not advance head state")
			}
			st = advanced
		}
	} else {
		st = headState
	}

	balances, total, err := extractBalanceInfo(ctx, st)
	if err != nil {
		return nil, err
	}
	return &confirmation.FFGStateInfo{
		TotalActiveBalance: total,
		Balances:           balances,
	}, nil
}
