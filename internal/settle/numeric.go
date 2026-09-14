package settle

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

// numericToBigInt converts a NUMERIC(39,0) column's scanned value to
// *big.Int. Every numeric column this package reads (cumulative_amount,
// last_settled_amount, charged_amount elsewhere) is declared with scale 0
// (§5), but that constrains the logical value, not how Postgres's wire
// encoding represents it: NUMERIC's binary format can express a whole
// number like 1000 as coefficient 1 with Exp=3 rather than coefficient
// 1000 with Exp=0 — confirmed the hard way in internal/indexer's own
// reconciliation code (see that package's reconcile.go), whose first
// version rejected any non-zero exponent and broke against a real SUM()
// result. A positive Exp is just scaling, handled here; a negative Exp
// would mean a genuine fractional value slipped into a scale-0 column,
// still worth erroring on rather than silently truncating money.
func numericToBigInt(n pgtype.Numeric) (*big.Int, error) {
	if !n.Valid {
		return nil, errors.New("numeric value is NULL")
	}
	if n.Int == nil {
		return big.NewInt(0), nil
	}
	v := new(big.Int).Set(n.Int)
	switch {
	case n.Exp > 0:
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil)
		v.Mul(v, scale)
	case n.Exp < 0:
		return nil, fmt.Errorf("numeric value has fractional exponent %d, want an integer (scale 0) column", n.Exp)
	}
	return v, nil
}
