package graph

import "testing"

func TestClassifyRemoval(t *testing.T) {
	const (
		archiveID   = "FOLDER_ARCHIVE"
		deletedID   = "FOLDER_DELETED"
		deletionsID = "FOLDER_DELETIONS" // Recoverable Items\Deletions
	)
	deleteIDs := []string{deletedID, deletionsID}
	tests := []struct {
		name           string
		parentFolderID string
		archiveID      string
		deleteIDs      []string
		notFound       bool
		want           RemovalKind
	}{
		{
			name:           "in archive folder",
			parentFolderID: archiveID,
			archiveID:      archiveID,
			deleteIDs:      deleteIDs,
			want:           RemovalArchive,
		},
		{
			name:           "in deleted items folder",
			parentFolderID: deletedID,
			archiveID:      archiveID,
			deleteIDs:      deleteIDs,
			want:           RemovalDelete,
		},
		{
			name:           "in recoverable-items deletions folder (Graph DELETE)",
			parentFolderID: deletionsID,
			archiveID:      archiveID,
			deleteIDs:      deleteIDs,
			want:           RemovalDelete,
		},
		{
			name:     "not found (404) always delete, even with empty folder ids",
			notFound: true,
			want:     RemovalDelete,
		},
		{
			name:           "not found wins over a matching archive folder",
			parentFolderID: archiveID,
			archiveID:      archiveID,
			deleteIDs:      deleteIDs,
			notFound:       true,
			want:           RemovalDelete,
		},
		{
			name:           "moved to a custom folder is unknown",
			parentFolderID: "SOME_OTHER_FOLDER",
			archiveID:      archiveID,
			deleteIDs:      deleteIDs,
			want:           RemovalUnknown,
		},
		{
			name:           "empty archive id never matches empty parent folder",
			parentFolderID: "",
			archiveID:      "",
			deleteIDs:      deleteIDs,
			want:           RemovalUnknown,
		},
		{
			name:           "empty delete ids never match empty parent folder",
			parentFolderID: "",
			archiveID:      archiveID,
			deleteIDs:      nil,
			want:           RemovalUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyRemoval(tt.parentFolderID, tt.archiveID, tt.deleteIDs, tt.notFound)
			if got != tt.want {
				t.Errorf("ClassifyRemoval(%q, %q, %v, %v) = %v, want %v",
					tt.parentFolderID, tt.archiveID, tt.deleteIDs, tt.notFound, got, tt.want)
			}
		})
	}
}

func TestRemovalKindString(t *testing.T) {
	cases := map[RemovalKind]string{
		RemovalArchive: "archive",
		RemovalDelete:  "delete",
		RemovalUnknown: "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("RemovalKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
