package psql

import (
	"github.com/KYVENetwork/tendermint/state/indexer"
	"github.com/KYVENetwork/tendermint/state/txindex"
)

var (
	_ indexer.BlockIndexer = BackportBlockIndexer{}
	_ txindex.TxIndexer    = BackportTxIndexer{}
)
