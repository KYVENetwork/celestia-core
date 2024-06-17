package psql

import (
	"github.com/KYVENetwork/celestia-core/state/indexer"
	"github.com/KYVENetwork/celestia-core/state/txindex"
)

var (
	_ indexer.BlockIndexer = BackportBlockIndexer{}
	_ txindex.TxIndexer    = BackportTxIndexer{}
)
