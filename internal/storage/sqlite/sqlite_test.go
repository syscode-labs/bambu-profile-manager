package sqlite_test

import (
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/storage"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
	"github.com/syscod3/bambu-profile-manager/internal/storage/storagetest"
)

func TestSQLiteRepositoryContract(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Repository {
		repo, err := sqlite.Open(":memory:")
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		t.Cleanup(func() { repo.Close() })
		return repo
	})
}
