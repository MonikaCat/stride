package client

import (
	"github.com/MonikaCat/stride/v15/x/stakeibc/client/cli"

	govclient "github.com/cosmos/cosmos-sdk/x/gov/client"
)

var (
	AddValidatorsProposalHandler = govclient.NewProposalHandler(cli.CmdAddValidatorsProposal)
	ToggleLSMProposalHandler     = govclient.NewProposalHandler(cli.CmdToggleLSMProposal)
)
