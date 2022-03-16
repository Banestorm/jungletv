package types

import (
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/gbl08ma/sqalx"
	"github.com/palantir/stacktrace"
	"github.com/shopspring/decimal"
)

// RewardBalance represents the balance of not-yet-withdrawn rewards for a user
type RewardBalance struct {
	RewardsAddress string          `db:"rewards_address" dbKey:"true"`
	Balance        decimal.Decimal `db:"balance"`
	UpdatedAt      time.Time       `db:"updated_at"`
}

// GetRewardBalanceOfAddress returns the reward balance for the specified address, if one exists
func GetRewardBalanceOfAddress(node sqalx.Node, address string) (*RewardBalance, error) {
	s := sdb.Select().
		Where(sq.Eq{"reward_balance.rewards_address": address})
	items, err := GetWithSelect[*RewardBalance](node, s)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	if len(items) == 0 {
		return &RewardBalance{
			RewardsAddress: address,
		}, nil
	}
	return items[0], nil
}

// GetRewardBalancesReadyForAutoWithdrawal returns rewards balances
// that are ready for automated withdrawal according to the passed parameters
func GetRewardBalancesReadyForAutoWithdrawal(node sqalx.Node, minBalance decimal.Decimal, unchangedSince time.Time) ([]*RewardBalance, error) {
	s := sdb.Select().
		Where(sq.Gt{"reward_balance.balance": decimal.Zero}).
		Where(sq.Or{
			sq.GtOrEq{"reward_balance.balance": minBalance},
			sq.Lt{"reward_balance.updated_at": unchangedSince},
		}).
		Where(sq.Expr("reward_balance.rewards_address NOT IN (SELECT rewards_address FROM pending_withdrawal)"))
	items, err := GetWithSelect[*RewardBalance](node, s)
	return items, stacktrace.Propagate(err, "")
}

// GetTotalOfRewardBalances returns the sum of all balances
func GetTotalOfRewardBalances(node sqalx.Node) (decimal.Decimal, error) {
	tx, err := node.Beginx()
	if err != nil {
		return decimal.Zero, stacktrace.Propagate(err, "")
	}
	defer tx.Commit() // read-only tx

	var tPtr *decimal.Decimal // the sum may be NULL if there are no rows
	err = sdb.Select("SUM(balance)").From("reward_balance").RunWith(tx).Scan(&tPtr)
	if tPtr == nil {
		return decimal.Zero, nil
	}
	return *tPtr, stacktrace.Propagate(err, "")
}

// AdjustRewardBalanceOfAddresses adjusts the balance of the specified addresses by the specified amount
func AdjustRewardBalanceOfAddresses(node sqalx.Node, addresses []string, amount decimal.Decimal) ([]*RewardBalance, error) {
	tx, err := node.Beginx()
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}
	defer tx.Rollback()

	now := time.Now()

	builder := sdb.Insert("reward_balance").Columns("rewards_address", "balance", "updated_at")
	for _, address := range addresses {
		builder = builder.Values(address, amount, now)
	}
	query, args, err := builder.Suffix(`
		ON CONFLICT (rewards_address)
		DO UPDATE SET balance = reward_balance.balance + EXCLUDED.balance, updated_at = EXCLUDED.updated_at
		RETURNING rewards_address, balance, updated_at`).
		ToSql()
	logger.Println(query, args, err)

	balances := []*RewardBalance{}
	err = tx.Tx().Select(&balances, query, args...)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	return balances, stacktrace.Propagate(tx.Commit(), "")
}

// ZeroRewardBalanceOfAddresses zeroes the balance of the specified addresses
func ZeroRewardBalanceOfAddresses(node sqalx.Node, addresses []string) error {
	tx, err := node.Beginx()
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	defer tx.Rollback()

	_, err = sdb.Update("reward_balance").
		Set("balance", decimal.Zero).
		Set("updated_at", time.Now()).
		Where(sq.Eq{"rewards_address": addresses}).
		RunWith(tx).Exec()
	if err != nil {
		return stacktrace.Propagate(err, "")
	}

	return stacktrace.Propagate(tx.Commit(), "")
}
