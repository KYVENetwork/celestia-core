package types

import cmtproto "github.com/KYVENetwork/celestia-core/proto/celestiacore/types"

// IsVoteTypeValid returns true if t is a valid vote type.
func IsVoteTypeValid(t cmtproto.SignedMsgType) bool {
	switch t {
	case cmtproto.PrevoteType, cmtproto.PrecommitType:
		return true
	default:
		return false
	}
}
