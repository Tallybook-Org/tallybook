package meter

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

// numericToBigInt converts a NUMERIC(39,0) column's scanned value to
// *big.Int. Duplicated from internal/settle's already-fixed version
// rather than shared across packages (matching the pattern
// internal/indexer and internal/settle already established): Postgres's
// NUMERIC wire encoding can express a whole number as a coefficient with
// a positive exponent rather than Exp=0 — internal/indexer's first
// version of this exact helper broke against a real SUM() result before
// that was accounted for; see that package's reconcile.go for the full
// story. A positive Exp is just scaling, handled here; a negative Exp
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
