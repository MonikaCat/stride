package types

import (
	errorsmod "cosmossdk.io/errors"
)

// x/autopilot module sentinel errors
var (
	ErrInvalidPacketMetadata     = errorsmod.Register(ModuleName, 1601, "invalid packet metadata")
	ErrUnsupportedStakeibcAction = errorsmod.Register(ModuleName, 1602, "unsupported stakeibc action")
	ErrInvalidClaimAirdropId     = errorsmod.Register(ModuleName, 1603, "invalid claim airdrop ID (cannot be empty)")
	ErrInvalidModuleRoutes       = errorsmod.Register(ModuleName, 1604, "invalid number of module routes, only 1 module is allowed at a time")
	ErrUnsupportedAutopilotRoute = errorsmod.Register(ModuleName, 1605, "unsupported autpilot route")
	ErrInvalidReceiverAddress    = errorsmod.Register(ModuleName, 1606, "receiver address must be specified when using autopilot")
	ErrPacketForwardingInactive  = errorsmod.Register(ModuleName, 1607, "autopilot packet forwarding is disabled")
	ErrInvalidMemoSize           = errorsmod.Register(ModuleName, 1608, "the memo or receiver field exceeded the max allowable size")
)
