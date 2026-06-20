package graph

import "testing"

func TestClassifyRemoval(t *testing.T) {
	const (
		archiveID = "FOLDER_ARCHIVE"
		deletedID = "FOLDER_DELETED"
	)
	tests := []struct {
		name           string
		parentFolderID string
		archiveID      string
		deletedID      string
		notFound       bool
		want           RemovalKind
	}{
		{
			name:           "in archive folder",
			parentFolderID: archiveID,
			archiveID:      archiveID,
			deletedID:      deletedID,
			want:           RemovalArchive,
		},
		{
			name:           "in deleted items folder",
			parentFolderID: deletedID,
			archiveID:      archiveID,
			deletedID:      deletedID,
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
			deletedID:      deletedID,
			notFound:       true,
			want:           RemovalDelete,
		},
		{
			name:           "moved to a custom folder is unknown",
			parentFolderID: "SOME_OTHER_FOLDER",
			archiveID:      archiveID,
			deletedID:      deletedID,
			want:           RemovalUnknown,
		},
		{
			name:           "empty archive id never matches empty parent folder",
			parentFolderID: "",
			archiveID:      "",
			deletedID:      deletedID,
			want:           RemovalUnknown,
		},
		{
			name:           "empty deleted id never matches empty parent folder",
			parentFolderID: "",
			archiveID:      archiveID,
			deletedID:      "",
			want:           RemovalUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyRemoval(tt.parentFolderID, tt.archiveID, tt.deletedID, tt.notFound)
			if got != tt.want {
				t.Errorf("ClassifyRemoval(%q, %q, %q, %v) = %v, want %v",
					tt.parentFolderID, tt.archiveID, tt.deletedID, tt.notFound, got, tt.want)
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
