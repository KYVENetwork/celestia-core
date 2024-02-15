package sr25519

import (
	"github.com/KYVENetwork/celestia-core/crypto"
	cmtjson "github.com/KYVENetwork/celestia-core/libs/json"
)

var _ crypto.PrivKey = PrivKey{}

const (
	PrivKeyName = "celestiacore/PrivKeySr25519"
	PubKeyName  = "celestiacore/PubKeySr25519"

	// SignatureSize is the size of an Edwards25519 signature. Namely the size of a compressed
	// Sr25519 point, and a field element. Both of which are 32 bytes.
	SignatureSize = 64
)

func init() {

	cmtjson.RegisterType(PubKey{}, PubKeyName)
	cmtjson.RegisterType(PrivKey{}, PrivKeyName)
}
