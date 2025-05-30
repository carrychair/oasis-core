package roothash

import (
	"fmt"

	"github.com/oasisprotocol/oasis-core/go/common"
	"github.com/oasisprotocol/oasis-core/go/common/crypto/signature"
	abciAPI "github.com/oasisprotocol/oasis-core/go/consensus/cometbft/api"
	registryState "github.com/oasisprotocol/oasis-core/go/consensus/cometbft/apps/registry/state"
	"github.com/oasisprotocol/oasis-core/go/consensus/cometbft/apps/roothash/api"
	roothashState "github.com/oasisprotocol/oasis-core/go/consensus/cometbft/apps/roothash/state"
	stakingState "github.com/oasisprotocol/oasis-core/go/consensus/cometbft/apps/staking/state"
	roothash "github.com/oasisprotocol/oasis-core/go/roothash/api"
	"github.com/oasisprotocol/oasis-core/go/roothash/api/commitment"
	"github.com/oasisprotocol/oasis-core/go/roothash/api/message"
	staking "github.com/oasisprotocol/oasis-core/go/staking/api"
)

// getRuntimeState fetches the current runtime state and performs common
// processing and error handling.
func (app *Application) getRuntimeState(
	ctx *abciAPI.Context,
	state *roothashState.MutableState,
	id common.Namespace,
) (*roothash.RuntimeState, error) {
	// Fetch current runtime state.
	rtState, err := state.RuntimeState(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("roothash: failed to fetch runtime state: %w", err)
	}
	if rtState.Suspended {
		return nil, roothash.ErrRuntimeSuspended
	}
	if rtState.Committee == nil {
		return nil, roothash.ErrNoCommittee
	}
	if rtState.CommitmentPool == nil {
		return nil, roothash.ErrNoExecutorPool
	}

	return rtState, nil
}

func (app *Application) executorCommit(
	ctx *abciAPI.Context,
	state *roothashState.MutableState,
	cc *roothash.ExecutorCommit,
) (err error) {
	if ctx.IsCheckOnly() {
		// Notify subscribers about observed commitments.
		for _, ec := range cc.Commits {
			app.ecn.DeliverExecutorCommitment(cc.ID, &ec)
		}
		return nil
	}

	// Charge gas for this transaction.
	params, err := state.ConsensusParameters(ctx)
	if err != nil {
		ctx.Logger().Error("ComputeCommit: failed to fetch consensus parameters",
			"err", err,
		)
		return err
	}
	if err = ctx.Gas().UseGas(1, roothash.GasOpComputeCommit, params.GasCosts); err != nil {
		return err
	}

	// Return early if there are no commitments.
	if len(cc.Commits) == 0 {
		return nil
	}

	// Fetch the latest runtime state.
	rtState, err := app.getRuntimeState(ctx, state, cc.ID)
	if err != nil {
		return err
	}
	prevRank := rtState.CommitmentPool.HighestRank

	// Node lookup needed for RAK-attestation.
	nl := registryState.NewMutableState(ctx.State())

	// Account for gas consumed by messages.
	msgGasAccountant := func(msgs []message.Message) error {
		// Deliver messages in the simulation context to estimate gas.
		msgCtx := ctx.WithSimulation()
		defer msgCtx.Close()

		_, msgErr := app.processRuntimeMessages(msgCtx, rtState, msgs)
		return msgErr
	}

	// Verify and add commitments to the pool.
	for _, commit := range cc.Commits {
		if err = commitment.VerifyExecutorCommitment(ctx, rtState.LastBlock, rtState.Runtime, rtState.Committee.ValidFor, &commit, msgGasAccountant, nl); err != nil { // nolint: gosec
			ctx.Logger().Debug("failed to verify executor commitment",
				"err", err,
				"runtime_id", cc.ID,
				"round", commit.Header.Header.Round,
			)
			return err
		}

		if err := rtState.CommitmentPool.AddVerifiedExecutorCommitment(rtState.Committee, &commit); err != nil { // nolint: gosec
			ctx.Logger().Debug("failed to add executor commitment",
				"err", err,
				"runtime_id", cc.ID,
				"round", commit.Header.Header.Round,
			)
			return err
		}

		ctx.Logger().Debug("executor commitment added to pool",
			"runtime_id", cc.ID,
			"round", commit.Header.Header.Round,
			"node_id", commit.NodeID,
			"scheduler_id", commit.Header.SchedulerID,
			"failure", commit.IsIndicatingFailure(),
		)
	}

	// Return early for simulation as we only need gas accounting.
	if ctx.IsSimulation() {
		return nil
	}

	// Commit changes made to the pool.
	ctx = ctx.NewTransaction()
	defer ctx.Close()

	state = roothashState.NewMutableState(ctx.State())

	// Check if higher-ranked scheduler submitted a commitment.
	if prevRank != rtState.CommitmentPool.HighestRank {
		round := rtState.LastBlock.Header.Round + 1

		ctx.Logger().Debug("transaction scheduler has changed",
			"runtime_id", cc.ID,
			"round", round,
			"prev_rank", prevRank,
			"new_rank", rtState.CommitmentPool.HighestRank,
		)

		// Re-arm round timeout. Give workers enough time to submit commitments.
		prevTimeout := rtState.NextTimeout
		rtState.NextTimeout = ctx.BlockHeight() + 1 + rtState.Runtime.Executor.RoundTimeout // Current height is ctx.BlockHeight() + 1

		if err := rearmRoundTimeout(ctx, cc.ID, round, prevTimeout, rtState.NextTimeout); err != nil {
			return err
		}
	}

	// Update runtime state.
	if err := state.SetRuntimeState(ctx, rtState); err != nil {
		return fmt.Errorf("failed to set runtime state: %w", err)
	}

	// Emit events for all accepted commits.
	for _, commit := range cc.Commits {
		ctx.EmitEvent(
			abciAPI.NewEventBuilder(app.Name()).
				TypedAttribute(&roothash.ExecutorCommittedEvent{Commit: commit}).
				TypedAttribute(&roothash.RuntimeIDAttribute{ID: cc.ID}),
		)
	}

	ctx.Commit()

	// Try to finalize the runtime during the end block.
	api.RegisterRuntimeForFinalization(ctx, cc.ID)

	return nil
}

func (app *Application) submitEvidence(
	ctx *abciAPI.Context,
	state *roothashState.MutableState,
	evidence *roothash.Evidence,
) error {
	// Validate proposal content basics.
	if err := evidence.ValidateBasic(); err != nil {
		ctx.Logger().Debug("Evidence: submitted evidence not valid",
			"evidence", evidence,
			"err", err,
		)
		return fmt.Errorf("%w: %v", roothash.ErrInvalidEvidence, err)
	}

	if ctx.IsCheckOnly() {
		return nil
	}

	// Charge gas for this transaction.
	params, err := state.ConsensusParameters(ctx)
	if err != nil {
		ctx.Logger().Error("Evidence: failed to fetch consensus parameters",
			"err", err,
		)
		return err
	}
	if err = ctx.Gas().UseGas(1, roothash.GasOpEvidence, params.GasCosts); err != nil {
		return err
	}

	// Return early for simulation as we only need gas accounting.
	if ctx.IsSimulation() {
		return nil
	}

	rtState, err := app.getRuntimeState(ctx, state, evidence.ID)
	if err != nil {
		return err
	}

	if len(rtState.Runtime.Staking.Slashing) == 0 {
		// No slashing instructions for runtime, no point in collecting evidence.
		ctx.Logger().Debug("Evidence: runtime has no slashing instructions",
			"err", roothash.ErrRuntimeDoesNotSlash,
		)
		return roothash.ErrRuntimeDoesNotSlash
	}
	slash := rtState.Runtime.Staking.Slashing[staking.SlashRuntimeEquivocation].Amount
	if slash.IsZero() {
		// Slash amount is zero for runtime, no point in collecting evidence.
		ctx.Logger().Debug("Evidence: runtime has no slashing instructions for equivocation",
			"err", roothash.ErrRuntimeDoesNotSlash,
		)
		return roothash.ErrRuntimeDoesNotSlash
	}

	// Ensure evidence is not expired.
	var round uint64
	var pk signature.PublicKey
	switch {
	case evidence.EquivocationExecutor != nil:
		commitA := evidence.EquivocationExecutor.CommitA

		if commitA.Header.Header.Round+params.MaxEvidenceAge < rtState.LastBlock.Header.Round {
			ctx.Logger().Debug("Evidence: commitment equivocation evidence expired",
				"evidence", evidence.EquivocationExecutor,
				"current_round", rtState.LastBlock.Header.Round,
				"max_evidence_age", params.MaxEvidenceAge,
			)
			return fmt.Errorf("%w: equivocation evidence expired", roothash.ErrInvalidEvidence)
		}
		round = commitA.Header.Header.Round
		pk = commitA.NodeID
	case evidence.EquivocationProposal != nil:
		proposalA := evidence.EquivocationProposal.ProposalA

		if proposalA.Header.Round+params.MaxEvidenceAge < rtState.LastBlock.Header.Round {
			ctx.Logger().Debug("Evidence: proposal equivocation evidence expired",
				"evidence", evidence.EquivocationExecutor,
				"current_round", rtState.LastBlock.Header.Round,
				"max_evidence_age", params.MaxEvidenceAge,
			)
			return fmt.Errorf("%w: equivocation evidence expired", roothash.ErrInvalidEvidence)
		}
		round = proposalA.Header.Round
		pk = proposalA.NodeID
	default:
		// This should never happen due to ValidateBasic check above.
		return roothash.ErrInvalidEvidence
	}

	// Evidence is valid. Store the evidence and slash the node.
	evHash, err := evidence.Hash()
	if err != nil {
		return fmt.Errorf("error computing evidence hash: %w", err)
	}
	b, err := state.ImmutableState.EvidenceHashExists(ctx, rtState.Runtime.ID, round, evHash)
	if err != nil {
		return fmt.Errorf("error querying evidence hash: %w", err)
	}
	if b {
		return roothash.ErrDuplicateEvidence
	}
	if err = state.SetEvidenceHash(ctx, rtState.Runtime.ID, round, evHash); err != nil {
		return err
	}

	if err = onEvidenceRuntimeEquivocation(
		ctx,
		pk,
		rtState.Runtime,
		&slash,
	); err != nil {
		return fmt.Errorf("error slashing runtime node: %w", err)
	}

	return nil
}

func (app *Application) submitMsg(
	ctx *abciAPI.Context,
	state *roothashState.MutableState,
	msg *roothash.SubmitMsg,
) error {
	if ctx.IsCheckOnly() {
		return nil
	}

	// Charge gas for this transaction.
	params, err := state.ConsensusParameters(ctx)
	if err != nil {
		ctx.Logger().Error("failed to fetch consensus parameters",
			"err", err,
		)
		return err
	}
	if err = ctx.Gas().UseGas(1, roothash.GasOpSubmitMsg, params.GasCosts); err != nil {
		return err
	}

	// Return early for simulation as we only need gas accounting.
	if ctx.IsSimulation() {
		return nil
	}

	rtState, err := app.getRuntimeState(ctx, state, msg.ID)
	if err != nil {
		return err
	}

	// If the maximum size of the queue is set to zero, bail early.
	if rtState.Runtime.TxnScheduler.MaxInMessages == 0 {
		return roothash.ErrIncomingMessageQueueFull
	}

	// If the submitted fee is smaller than the minimum fee, bail early.
	if msg.Fee.Cmp(&rtState.Runtime.Staking.MinInMessageFee) < 0 {
		return roothash.ErrIncomingMessageInsufficientFee
	}

	// Create a new transaction context and rollback in case we fail.
	ctx = ctx.NewTransaction()
	defer ctx.Close()

	// Transfer the given amount (fee + tokens) into the runtime account.
	totalAmount := msg.Fee.Clone()
	if err = totalAmount.Add(&msg.Tokens); err != nil {
		return err
	}

	st := stakingState.NewMutableState(ctx.State())
	rtAddress := staking.NewRuntimeAddress(rtState.Runtime.ID)
	if err = st.Transfer(ctx, ctx.CallerAddress(), rtAddress, totalAmount); err != nil {
		return err
	}

	// Fetch current incoming queue metadata.
	meta, err := state.IncomingMessageQueueMeta(ctx, rtState.Runtime.ID)
	if err != nil {
		return err
	}

	// Check if the queue is already full.
	if meta.Size >= rtState.Runtime.TxnScheduler.MaxInMessages {
		return roothash.ErrIncomingMessageQueueFull
	}

	// Queue message.
	inMsg := &message.IncomingMessage{
		ID:     meta.NextSequenceNumber,
		Caller: ctx.CallerAddress(),
		Tag:    msg.Tag,
		Fee:    msg.Fee,
		Tokens: msg.Tokens,
		Data:   msg.Data,
	}
	if err = state.SetIncomingMessageInQueue(ctx, rtState.Runtime.ID, inMsg); err != nil {
		return err
	}

	// Update next sequence number.
	meta.Size++
	meta.NextSequenceNumber++
	if err = state.SetIncomingMessageQueueMeta(ctx, rtState.Runtime.ID, meta); err != nil {
		return err
	}

	ctx.Commit()

	return nil
}
