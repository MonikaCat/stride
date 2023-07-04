package icaoracle

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	capabilitytypes "github.com/cosmos/cosmos-sdk/x/capability/types"
	channeltypes "github.com/cosmos/ibc-go/v7/modules/core/04-channel/types"
	porttypes "github.com/cosmos/ibc-go/v7/modules/core/05-port/types"
	ibcexported "github.com/cosmos/ibc-go/v7/modules/core/exported"

	"github.com/Stride-Labs/stride/v11/x/icaoracle/keeper"
)

var _ porttypes.Middleware = &IBCMiddleware{}

type IBCMiddleware struct {
	app    porttypes.IBCModule
	keeper keeper.Keeper
}

// NewIBCMiddleware creates a new IBCMiddleware given the keeper
func NewIBCMiddleware(app porttypes.IBCModule, k keeper.Keeper) IBCMiddleware {
	return IBCMiddleware{
		app:    app,
		keeper: k,
	}
}

// OnChanOpenInit implements the IBCMiddleware interface
func (im IBCMiddleware) OnChanOpenInit(
	ctx sdk.Context,
	order channeltypes.Order,
	connectionHops []string,
	portID string,
	channelID string,
	channelCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	version string,
) (string, error) {
	im.keeper.Logger(ctx).Info(fmt.Sprintf("OnChanOpenAck (ICAOracle): portID %s, channelID %s", portID, channelID))

	if err := im.keeper.OnChanOpenInit(ctx, portID, channelID, channelCap); err != nil {
		im.keeper.Logger(ctx).Error(fmt.Sprintf("ICAOracle ChanOpenInit failed: %s", err.Error()))
		return version, errorsmod.Wrapf(err, "ICAOracle ChanOpenInit failed")
	}

	return im.app.OnChanOpenInit(
		ctx,
		order,
		connectionHops,
		portID,
		channelID,
		channelCap,
		counterparty,
		version,
	)
}

// OnChanOpenAck implements the IBCMiddleware interface
func (im IBCMiddleware) OnChanOpenAck(
	ctx sdk.Context,
	portID string,
	channelID string,
	counterpartyChannelID string,
	counterpartyVersion string,
) error {
	im.keeper.Logger(ctx).Info(fmt.Sprintf("OnChanOpenAck (ICAOracle): portID %s, channelID %s, counterpartyChannelID %s, counterpartyVersion %s",
		portID, channelID, counterpartyChannelID, counterpartyVersion))

	if err := im.keeper.OnChanOpenAck(ctx, portID, channelID); err != nil {
		im.keeper.Logger(ctx).Error(fmt.Sprintf("ICAOracle ChanOpenAck failed: %s", err.Error()))
		return errorsmod.Wrapf(err, "ICAOracle ChanOpenInit failed")
	}

	return im.app.OnChanOpenAck(
		ctx,
		portID,
		channelID,
		counterpartyChannelID,
		counterpartyVersion,
	)
}

// OnChanCloseConfirm implements the IBCMiddleware interface
func (im IBCMiddleware) OnChanCloseConfirm(
	ctx sdk.Context,
	portID,
	channelID string,
) error {
	return nil
}

// OnAcknowledgementPacket implements the IBCMiddleware interface
func (im IBCMiddleware) OnAcknowledgementPacket(
	ctx sdk.Context,
	packet channeltypes.Packet,
	acknowledgement []byte,
	relayer sdk.AccAddress,
) error {
	im.keeper.Logger(ctx).Info(fmt.Sprintf("OnAcknowledgementPacket (ICAOracle): Sequence %d, SourcePort %s, SourceChannel %s, DestinationPort %s, DestinationChannel %s",
		packet.Sequence, packet.SourcePort, packet.SourceChannel, packet.DestinationPort, packet.DestinationChannel))

	if err := im.keeper.OnAcknowledgementPacket(ctx, packet, acknowledgement); err != nil {
		im.keeper.Logger(ctx).Error(fmt.Sprintf("ICAOracle OnAcknowledementPacket failed: %s", err.Error()))
		return errorsmod.Wrapf(err, "ICAOracle OnAcknowledementPacket failed")
	}

	return im.app.OnAcknowledgementPacket(ctx, packet, acknowledgement, relayer)
}

// OnTimeoutPacket implements the IBCMiddleware interface
func (im IBCMiddleware) OnTimeoutPacket(ctx sdk.Context, packet channeltypes.Packet, relayer sdk.AccAddress) error {
	im.keeper.Logger(ctx).Info(fmt.Sprintf("OnTimeoutPacket (ICAOracle): packet %v, relayer %v", packet, relayer))

	// TODO: Call ICA callbacks with timeout

	return im.app.OnTimeoutPacket(ctx, packet, relayer)
}

// OnChanOpenTry implements the IBCMiddleware interface.
func (im IBCMiddleware) OnChanOpenTry(
	ctx sdk.Context,
	order channeltypes.Order,
	connectionHops []string,
	portID,
	channelID string,
	chanCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	counterpartyVersion string,
) (string, error) {
	panic("UNIMPLEMENTED")
}

// OnChanOpenConfirm implements the IBCMiddleware interface
func (im IBCMiddleware) OnChanOpenConfirm(
	ctx sdk.Context,
	portID,
	channelID string,
) error {
	panic("UNIMPLEMENTED")
}

// OnChanCloseInit implements the IBCMiddleware interface
func (im IBCMiddleware) OnChanCloseInit(
	ctx sdk.Context,
	portID,
	channelID string,
) error {
	panic("UNIMPLEMENTED")
}

// OnRecvPacket implements the IBCMiddleware interface
func (im IBCMiddleware) OnRecvPacket(
	ctx sdk.Context,
	packet channeltypes.Packet,
	relayer sdk.AccAddress,
) ibcexported.Acknowledgement {
	panic("UNIMPLEMENTED")
}

// Send implements the ICS4Wrapper interface
func (im IBCMiddleware) SendPacket(
	ctx sdk.Context,
	chanCap *capabilitytypes.Capability,
	sourcePort string,
	sourceChannel string,
	timeoutHeight clienttypes.Height,
	timeoutTimestamp uint64,
	data []byte,
) (sequence uint64, err error) {
	return im.keeper.ICS4Wrapper.SendPacket(
		ctx,
		chanCap,
		sourcePort,
		sourceChannel,
		timeoutHeight,
		timeoutTimestamp,
		data,
	)
}

// WriteAcknowledgement implements the ICS4Wrapper interface
func (im IBCMiddleware) WriteAcknowledgement(
	ctx sdk.Context,
	chanCap *capabilitytypes.Capability,
	packet ibcexported.PacketI,
	ack ibcexported.Acknowledgement,
) error {
	return im.keeper.ICS4Wrapper.WriteAcknowledgement(ctx, chanCap, packet, ack)
}

// GetAppVersion implements the ICS4Wrapper interface
func (im IBCMiddleware) GetAppVersion(ctx sdk.Context, portID, channelID string) (string, bool) {
	return im.keeper.ICS4Wrapper.GetAppVersion(ctx, portID, channelID)
}
