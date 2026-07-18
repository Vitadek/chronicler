package replica

import "context"

type ReplicaObjectMetadata struct {
	Key         string     `json:"key"`
	Size        *int64     `json:"size,omitempty"`
	ContentType *string    `json:"contentType,omitempty"`
	Checksum    *string    `json:"checksum,omitempty"`
	Generation  *int       `json:"generation,omitempty"`
	ETag        *string    `json:"etag,omitempty"`
	UpdatedAt   *int64     `json:"updatedAt,omitempty"` // milliseconds timestamp
}

type ReplicaPutOptions struct {
	ContentType string
	Checksum    string
	Generation  int
}

type ReplicaProvider interface {
	Name() string
	Initialize(ctx context.Context) error
	Put(ctx context.Context, key string, content []byte, opts ReplicaPutOptions) error
	Head(ctx context.Context, key string) (*ReplicaObjectMetadata, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]ReplicaObjectMetadata, error)
	Close() error
}
