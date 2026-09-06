package main

import "testing"

func TestKnowledgeMigrationsUpgradePreservesTasksAndReruns(t *testing.T) {
	db := nativeGovernanceDB(t, "knowledge-upgrade.db")
	applyNativeGovernanceMigrations(t, db, "0043_coordinator_repair_semantic.sql")
	assertSQLiteObjectAbsent(t, db, "table", "knowledge_items")
	seedNativeGovernanceFixtures(t, db)
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM work_items`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, db, "")
	for _, table := range []string{"knowledge_items", "knowledge_versions", "knowledge_relations", "knowledge_sources", "knowledge_submissions", "knowledge_jobs", "knowledge_job_actions", "knowledge_run_captures", "knowledge_librarian_configs"} {
		assertNativeGovernanceTable(t, db, table)
	}
	assertNativeColumns(t, db, "knowledge_query_snapshots", "scope_json")
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM work_items`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before == 0 || before != after {
		t.Fatalf("knowledge migration changed existing task rows: %d -> %d", before, after)
	}
	var versions int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	applyNativeGovernanceMigrations(t, db, "")
	var replayed int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&replayed); err != nil {
		t.Fatal(err)
	}
	if versions != replayed {
		t.Fatalf("migration replay changed applied versions: %d -> %d", versions, replayed)
	}
}
