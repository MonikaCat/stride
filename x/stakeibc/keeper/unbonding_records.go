package keeper

import (
	"fmt"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/golang/protobuf/proto" //nolint:staticcheck

	errorsmod "cosmossdk.io/errors"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/spf13/cast"

	"github.com/Stride-Labs/stride/v9/utils"
	recordstypes "github.com/Stride-Labs/stride/v9/x/records/types"
	"github.com/Stride-Labs/stride/v9/x/stakeibc/types"
)

func (k Keeper) CreateEpochUnbondingRecord(ctx sdk.Context, epochNumber uint64) bool {
	k.Logger(ctx).Info(fmt.Sprintf("Creating Epoch Unbonding Records for Epoch %d", epochNumber))

	hostZoneUnbondings := []*recordstypes.HostZoneUnbonding{}

	for _, hostZone := range k.GetAllActiveHostZone(ctx) {
		k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Creating Epoch Unbonding Record"))

		hostZoneUnbonding := recordstypes.HostZoneUnbonding{
			NativeTokenAmount: sdkmath.ZeroInt(),
			StTokenAmount:     sdkmath.ZeroInt(),
			Denom:             hostZone.HostDenom,
			HostZoneId:        hostZone.ChainId,
			Status:            recordstypes.HostZoneUnbonding_UNBONDING_QUEUE,
		}
		hostZoneUnbondings = append(hostZoneUnbondings, &hostZoneUnbonding)
	}

	epochUnbondingRecord := recordstypes.EpochUnbondingRecord{
		EpochNumber:        cast.ToUint64(epochNumber),
		HostZoneUnbondings: hostZoneUnbondings,
	}
	k.RecordsKeeper.SetEpochUnbondingRecord(ctx, epochUnbondingRecord)
	return true
}

// Gets the total unbonded amount for a host zone by looping through the epoch unbonding records
// Also returns the epoch unbonding record ids
func (k Keeper) GetTotalUnbondAmountAndRecordsIds(ctx sdk.Context, chainId string) (totalUnbonded sdkmath.Int, unbondingRecordIds []uint64) {
	totalUnbonded = sdk.ZeroInt()
	for _, epochUnbonding := range k.RecordsKeeper.GetAllEpochUnbondingRecord(ctx) {
		hostZoneRecord, found := k.RecordsKeeper.GetHostZoneUnbondingByChainId(ctx, epochUnbonding.EpochNumber, chainId)
		if !found {
			continue
		}
		k.Logger(ctx).Info(utils.LogWithHostZone(chainId, "Epoch %d - Status: %s, Amount: %v",
			epochUnbonding.EpochNumber, hostZoneRecord.Status, hostZoneRecord.NativeTokenAmount))

		// We'll unbond all records that have status UNBONDING_QUEUE and have an amount g.t. zero
		if hostZoneRecord.Status == recordstypes.HostZoneUnbonding_UNBONDING_QUEUE && hostZoneRecord.NativeTokenAmount.GT(sdkmath.ZeroInt()) {
			totalUnbonded = totalUnbonded.Add(hostZoneRecord.NativeTokenAmount)
			unbondingRecordIds = append(unbondingRecordIds, epochUnbonding.EpochNumber)
			k.Logger(ctx).Info(utils.LogWithHostZone(chainId, "  %v%s included in total unbonding", hostZoneRecord.NativeTokenAmount, hostZoneRecord.Denom))
		}
	}
	return totalUnbonded, unbondingRecordIds
}

// Given a list of target delegation changes, builds the individual unbonding messages by redelegating
// from surplus validators to deficit validators
// Returns the list of messages and the callback data for the ICA
func (k Keeper) GetUnbondingICAMessages(
	hostZone types.HostZone,
	unbondingsByValidator map[string]sdkmath.Int,
) (msgs []sdk.Msg, unbondings []*types.SplitDelegation) {
	for _, validatorAddress := range utils.StringMapKeys(unbondingsByValidator) { // DO NOT REMOVE: StringMapKeys fixes non-deterministic map iteration
		undelegationAmount := sdk.NewCoin(hostZone.HostDenom, unbondingsByValidator[validatorAddress])

		// Ignore validators with a zero undelegation amount to prevent a failed transaction on the host
		if undelegationAmount.IsZero() {
			continue
		}

		// Store the ICA transactions
		msgs = append(msgs, &stakingtypes.MsgUndelegate{
			DelegatorAddress: hostZone.DelegationIcaAddress,
			ValidatorAddress: validatorAddress,
			Amount:           undelegationAmount,
		})

		// Store the split delegations for the callback
		unbondings = append(unbondings, &types.SplitDelegation{
			Validator: validatorAddress,
			Amount:    undelegationAmount.Amount,
		})
	}

	return msgs, unbondings
}

// Build the undelegation messages for each validator by summing the total amount to unbond across epoch unbonding record,
//   and then splitting the undelegation amount across validators
// returns (1) MsgUndelegate messages
//         (2) Total Amount to unbond across all validators
//         (3) Marshalled Callback Args
//         (4) Relevant EpochUnbondingRecords that contain HostZoneUnbondings that are ready for unbonding
func (k Keeper) UnbondFromHostZone(ctx sdk.Context, hostZone types.HostZone) error {
	k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Preparing MsgUndelegates from the delegation account to each validator"))

	// Iterate through every unbonding record and sum the total amount to unbond for the given host zone
	totalAmountToUnbond, epochUnbondingRecordIds := k.GetTotalUnbondAmountAndRecordsIds(ctx, hostZone.ChainId)
	k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Total unbonded amount: %v%s", totalAmountToUnbond, hostZone.HostDenom))

	// If there's nothing to unbond, return and move on to the next host zone
	if totalAmountToUnbond.IsZero() {
		return nil
	}

	// Determine the desired unbonding amount for each validator based based on our target weights
	targetUnbondingsByValidator, err := k.GetTargetValAmtsForHostZone(ctx, hostZone, totalAmountToUnbond)
	if err != nil {
		return errorsmod.Wrapf(err, "unable to get target val amounts for host zone from total %v", totalAmountToUnbond)
	}

	// Check if each validator has enough current delegations to cover the target unbonded amount
	// If it doesn't have enough, update the target to equal their total delegations and record the overflow amount
	finalUnbondingsByValidator := make(map[string]sdkmath.Int)
	overflowAmount := sdkmath.ZeroInt()
	for _, validator := range hostZone.Validators {

		targetUnbondAmount := targetUnbondingsByValidator[validator.Address]
		k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId,
			"  Validator %s - Weight: %d, Target Unbond Amount: %v, Current Delegations: %v", validator.Address, validator.Weight, targetUnbondAmount, validator.Delegation))

		// If they don't have enough to cover the unbondings, set their target unbond amount to their current delegations and increment the overflow
		if targetUnbondAmount.GT(validator.Delegation) {
			overflowAmount = overflowAmount.Add(targetUnbondAmount).Sub(validator.Delegation)
			targetUnbondAmount = validator.Delegation
		}
		finalUnbondingsByValidator[validator.Address] = targetUnbondAmount
	}

	// If there was overflow (i.e. there was at least one validator without sufficient delegations to cover their unbondings)
	//  then reallocate across the other validators
	if overflowAmount.GT(sdkmath.ZeroInt()) {
		k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId,
			"Expected validator undelegation amount on is greater than it's current delegations. Redistributing undelegations accordingly."))

		for _, validator := range hostZone.Validators {
			targetUnbondAmount := finalUnbondingsByValidator[validator.Address]

			// Check if we can unbond more from this validator
			validatorUnbondExtraCapacity := validator.Delegation.Sub(targetUnbondAmount)
			if validatorUnbondExtraCapacity.GT(sdkmath.ZeroInt()) {

				// If we can fully cover the unbonding, do so with this validator
				if validatorUnbondExtraCapacity.GT(overflowAmount) {
					finalUnbondingsByValidator[validator.Address] = finalUnbondingsByValidator[validator.Address].Add(overflowAmount)
					overflowAmount = sdkmath.ZeroInt()
					break
				} else {
					// If we can't, cover the unbondings, cover as much as we can and move onto the next validator
					finalUnbondingsByValidator[validator.Address] = finalUnbondingsByValidator[validator.Address].Add(validatorUnbondExtraCapacity)
					overflowAmount = overflowAmount.Sub(validatorUnbondExtraCapacity)
				}
			}
		}
	}

	// If after re-allocating, we still can't cover the overflow, something is very wrong
	if overflowAmount.GT(sdkmath.ZeroInt()) {
		return fmt.Errorf("Could not unbond %v on Host Zone %s, unable to balance the unbond amount across validators",
			totalAmountToUnbond, hostZone.ChainId)
	}

	// Get the delegation account
	if hostZone.DelegationIcaAddress == "" {
		return errorsmod.Wrapf(types.ErrICAAccountNotFound, "no delegation account found for %s", hostZone.ChainId)
	}

	// Construct the MsgUndelegate transaction and callback data
	msgs, splitDelegations := k.GetUnbondingICAMessages(hostZone, finalUnbondingsByValidator)

	// Shouldn't be possible, but if all the validator's had a target unbonding of zero, do not send an ICA
	if len(msgs) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "Target unbonded amount was 0 for each validator")
	}

	// Store the callback data
	undelegateCallback := types.UndelegateCallback{
		HostZoneId:              hostZone.ChainId,
		SplitDelegations:        splitDelegations,
		EpochUnbondingRecordIds: epochUnbondingRecordIds,
	}
	callbackArgsBz, err := proto.Marshal(&undelegateCallback)
	if err != nil {
		return errorsmod.Wrap(err, "unable to marshal undelegate callback args")
	}

	if _, err := k.SubmitTxsDayEpoch(
		ctx,
		hostZone.ConnectionId,
		msgs,
		types.ICAAccountType_DELEGATION,
		ICACallbackID_Undelegate,
		callbackArgsBz,
	); err != nil {
		return errorsmod.Wrapf(err, "unable to submit unbonding ICA for %s", hostZone.ChainId)
	}

	if err := k.RecordsKeeper.SetHostZoneUnbondings(
		ctx,
		hostZone.ChainId,
		epochUnbondingRecordIds,
		recordstypes.HostZoneUnbonding_UNBONDING_IN_PROGRESS,
	); err != nil {
		return err
	}

	// TODO [LSM]: Move to events.go
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute("hostZone", hostZone.ChainId),
			sdk.NewAttribute("newAmountUnbonding", totalAmountToUnbond.String()),
		),
	)

	return nil
}

// this function iterates each host zone, and if it's the right time to
// initiate an unbonding, it attempts to unbond all outstanding records
// returns (1) did all chains succeed
//		   (2) list of strings of successful unbondings
//		   (3) list of strings of failed unbondings
func (k Keeper) InitiateAllHostZoneUnbondings(ctx sdk.Context, dayNumber uint64) (success bool, successfulUnbondings []string, failedUnbondings []string) {
	k.Logger(ctx).Info(fmt.Sprintf("Initiating all host zone unbondings for epoch %d...", dayNumber))

	success = true
	successfulUnbondings = []string{}
	failedUnbondings = []string{}
	for _, hostZone := range k.GetAllActiveHostZone(ctx) {

		// Confirm the unbonding is supposed to be triggered this epoch
		unbondingFrequency := hostZone.GetUnbondingFrequency()
		if dayNumber%unbondingFrequency != 0 {
			k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId,
				"Host does not unbond this epoch (Unbonding Period: %d, Unbonding Frequency: %d, Epoch: %d)",
				hostZone.UnbondingPeriod, unbondingFrequency, dayNumber))
			continue
		}

		// Get host zone unbonding message by summing up the unbonding records
		if err := k.UnbondFromHostZone(ctx, hostZone); err != nil {
			k.Logger(ctx).Error(fmt.Sprintf("Error initiating host zone unbondings for host zone %s: %s", hostZone.ChainId, err.Error()))
			success = false
			failedUnbondings = append(failedUnbondings, hostZone.ChainId)
			continue
		}

		successfulUnbondings = append(successfulUnbondings, hostZone.ChainId)
	}

	return success, successfulUnbondings, failedUnbondings
}

// Deletes any epoch unbonding records that have had all unbondings claimed
func (k Keeper) CleanupEpochUnbondingRecords(ctx sdk.Context, epochNumber uint64) bool {
	k.Logger(ctx).Info("Cleaning Claimed Epoch Unbonding Records...")

	for _, epochUnbondingRecord := range k.RecordsKeeper.GetAllEpochUnbondingRecord(ctx) {
		shouldDeleteEpochUnbondingRecord := true
		hostZoneUnbondings := epochUnbondingRecord.HostZoneUnbondings

		for _, hostZoneUnbonding := range hostZoneUnbondings {
			// if an EpochUnbondingRecord has any HostZoneUnbonding with non-zero balances, we don't delete the EpochUnbondingRecord
			// because it has outstanding tokens that need to be claimed
			if !hostZoneUnbonding.NativeTokenAmount.Equal(sdkmath.ZeroInt()) {
				shouldDeleteEpochUnbondingRecord = false
				break
			}
		}
		if shouldDeleteEpochUnbondingRecord {
			k.Logger(ctx).Info(fmt.Sprintf("  EpochUnbondingRecord %d - All unbondings claimed, removing record", epochUnbondingRecord.EpochNumber))
			k.RecordsKeeper.RemoveEpochUnbondingRecord(ctx, epochUnbondingRecord.EpochNumber)
		} else {
			k.Logger(ctx).Info(fmt.Sprintf("  EpochUnbondingRecord %d - Has unclaimed unbondings", epochUnbondingRecord.EpochNumber))
		}
	}

	return true
}

// Batch transfers any unbonded tokens from the delegation account to the redemption account
func (k Keeper) SweepAllUnbondedTokensForHostZone(ctx sdk.Context, hostZone types.HostZone, epochUnbondingRecords []recordstypes.EpochUnbondingRecord) (success bool, sweepAmount sdkmath.Int) {
	k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Sweeping unbonded tokens"))

	// Sum up all host zone unbonding records that have finished unbonding
	totalAmtTransferToRedemptionAcct := sdkmath.ZeroInt()
	epochUnbondingRecordIds := []uint64{}
	for _, epochUnbondingRecord := range epochUnbondingRecords {

		// Get all the unbondings associated with the epoch + host zone pair
		hostZoneUnbonding, found := k.RecordsKeeper.GetHostZoneUnbondingByChainId(ctx, epochUnbondingRecord.EpochNumber, hostZone.ChainId)
		if !found {
			continue
		}

		// Get latest blockTime from light client
		blockTime, err := k.GetLightClientTimeSafely(ctx, hostZone.ConnectionId)
		if err != nil {
			k.Logger(ctx).Error(fmt.Sprintf("\tCould not find blockTime for host zone %s", hostZone.ChainId))
			continue
		}

		k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Epoch %d - Status: %s, Amount: %v, Unbonding Time: %d, Block Time: %d",
			epochUnbondingRecord.EpochNumber, hostZoneUnbonding.Status.String(), hostZoneUnbonding.NativeTokenAmount, hostZoneUnbonding.UnbondingTime, blockTime))

		// If the unbonding period has elapsed, then we can send the ICA call to sweep this
		//   hostZone's unbondings to the redemption account (in a batch).
		// Verify:
		//      1. the unbonding time is set (g.t. 0)
		//      2. the unbonding time is less than the current block time
		//      3. the host zone is in the EXIT_TRANSFER_QUEUE state, meaning it's ready to be transferred
		inTransferQueue := hostZoneUnbonding.Status == recordstypes.HostZoneUnbonding_EXIT_TRANSFER_QUEUE
		validUnbondingTime := hostZoneUnbonding.UnbondingTime > 0 && hostZoneUnbonding.UnbondingTime < blockTime
		if inTransferQueue && validUnbondingTime {
			k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "  %v%s included in sweep", hostZoneUnbonding.NativeTokenAmount, hostZoneUnbonding.Denom))

			if err != nil {
				errMsg := fmt.Sprintf("Could not convert native token amount to int64 | %s", err.Error())
				k.Logger(ctx).Error(errMsg)
				continue
			}
			totalAmtTransferToRedemptionAcct = totalAmtTransferToRedemptionAcct.Add(hostZoneUnbonding.NativeTokenAmount)
			epochUnbondingRecordIds = append(epochUnbondingRecordIds, epochUnbondingRecord.EpochNumber)
		}
	}

	// If we have any amount to sweep, then we can send the ICA call to sweep them
	if totalAmtTransferToRedemptionAcct.LTE(sdkmath.ZeroInt()) {
		k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "No tokens ready for sweep"))
		return true, totalAmtTransferToRedemptionAcct
	}
	k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Batch transferring %v to host zone", totalAmtTransferToRedemptionAcct))

	// Get the delegation account and redemption account
	if hostZone.DelegationIcaAddress == "" {
		k.Logger(ctx).Error(fmt.Sprintf("Zone %s is missing a delegation address!", hostZone.ChainId))
		return false, sdkmath.ZeroInt()
	}
	if hostZone.RedemptionIcaAddress == "" {
		k.Logger(ctx).Error(fmt.Sprintf("Zone %s is missing a redemption address!", hostZone.ChainId))
		return false, sdkmath.ZeroInt()
	}

	// Build transfer message to transfer from the delegation account to redemption account
	sweepCoin := sdk.NewCoin(hostZone.HostDenom, totalAmtTransferToRedemptionAcct)
	msgs := []sdk.Msg{
		&banktypes.MsgSend{
			FromAddress: hostZone.DelegationIcaAddress,
			ToAddress:   hostZone.RedemptionIcaAddress,
			Amount:      sdk.NewCoins(sweepCoin),
		},
	}
	k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "Preparing MsgSend from Delegation Account to Redemption Account"))

	// Store the epoch numbers in the callback to identify the epoch unbonding records
	redemptionCallback := types.RedemptionCallback{
		HostZoneId:              hostZone.ChainId,
		EpochUnbondingRecordIds: epochUnbondingRecordIds,
	}
	marshalledCallbackArgs, err := k.MarshalRedemptionCallbackArgs(ctx, redemptionCallback)
	if err != nil {
		k.Logger(ctx).Error(err.Error())
		return false, sdkmath.ZeroInt()
	}

	// Send the transfer ICA
	_, err = k.SubmitTxsDayEpoch(ctx, hostZone.ConnectionId, msgs, types.ICAAccountType_DELEGATION, ICACallbackID_Redemption, marshalledCallbackArgs)
	if err != nil {
		k.Logger(ctx).Error(fmt.Sprintf("Failed to SubmitTxs, transfer to redemption account on %s", hostZone.ChainId))
		return false, sdkmath.ZeroInt()
	}
	k.Logger(ctx).Info(utils.LogWithHostZone(hostZone.ChainId, "ICA MsgSend Successfully Sent"))

	// Update the host zone unbonding records to status IN_PROGRESS
	err = k.RecordsKeeper.SetHostZoneUnbondings(ctx, hostZone.ChainId, epochUnbondingRecordIds, recordstypes.HostZoneUnbonding_EXIT_TRANSFER_IN_PROGRESS)
	if err != nil {
		k.Logger(ctx).Error(err.Error())
		return false, sdkmath.ZeroInt()
	}

	return true, totalAmtTransferToRedemptionAcct
}

// Sends all unbonded tokens to the redemption account
//    returns:
//       * success indicator if all chains succeeded
//       * list of successful chains
//       * list of tokens swept
//       * list of failed chains
func (k Keeper) SweepAllUnbondedTokens(ctx sdk.Context) (success bool, successfulSweeps []string, sweepAmounts []sdkmath.Int, failedSweeps []string) {
	// this function returns true if all chains succeeded, false otherwise
	// it also returns a list of successful chains (arg 2), tokens swept (arg 3), and failed chains (arg 4)
	k.Logger(ctx).Info("Sweeping All Unbonded Tokens...")

	success = true
	successfulSweeps = []string{}
	sweepAmounts = []sdkmath.Int{}
	failedSweeps = []string{}
	hostZones := k.GetAllActiveHostZone(ctx)

	epochUnbondingRecords := k.RecordsKeeper.GetAllEpochUnbondingRecord(ctx)
	for _, hostZone := range hostZones {
		hostZoneSuccess, sweepAmount := k.SweepAllUnbondedTokensForHostZone(ctx, hostZone, epochUnbondingRecords)
		if hostZoneSuccess {
			successfulSweeps = append(successfulSweeps, hostZone.ChainId)
			sweepAmounts = append(sweepAmounts, sweepAmount)
		} else {
			success = false
			failedSweeps = append(failedSweeps, hostZone.ChainId)
		}
	}

	return success, successfulSweeps, sweepAmounts, failedSweeps
}
