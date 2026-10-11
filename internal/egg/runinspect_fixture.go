//go:build runinspectfixture

package egg

// FixturePersistRunTurn exercises the durable writer without launching an egg.
// This adapter exists only in explicitly tagged fixture/test builds.
func FixturePersistRunTurn(dir, hash string, result RunTurnResult) error {
	return persistRunTurn(dir, runTurnRecord{Version: 1, SpecHash: hash, Result: result})
}
