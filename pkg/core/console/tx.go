package console

import (
	"fmt"
	"strings"

	"github.com/AudiusProject/audiusd/pkg/core/console/views/pages"
	"github.com/labstack/echo/v4"
)

func (con *Console) txPage(c echo.Context) error {
	ctx := c.Request().Context()
	txhash := c.Param("tx")

	tx, err := con.db.GetTx(ctx, strings.ToUpper(txhash))
	if err != nil {
		con.logger.Errorf("err getting tx: %v", err)
		return err
	}

	data := &pages.TxView{
		Hash:      txhash,
		Block:     fmt.Sprint(tx.BlockID),
		Timestamp: tx.CreatedAt.Time,
		Tx:        tx.Transaction,
	}

	return con.views.RenderTxView(c, data)
}
