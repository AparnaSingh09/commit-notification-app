// Package db handles the MongoDB connection and one-time index setup.
package db

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Connect dials MongoDB and returns a handle to the app's database.
// It requires a replica set (even single-node) since later steps use
// multi-document transactions when adding a repo subscription.
func Connect(ctx context.Context, uri, dbName string) (*mongo.Database, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connecting to mongo: %w", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("pinging mongo: %w", err)
	}

	return client.Database(dbName), nil
}

// EnsureIndexes creates the unique indexes the app relies on for dedupe.
// Safe to call on every startup - creating an existing index is a no-op.
func EnsureIndexes(ctx context.Context, database *mongo.Database) error {
	type indexSpec struct {
		collection string
		keys       bson.D
		name       string
	}

	specs := []indexSpec{
		{
			collection: "users",
			keys:       bson.D{{Key: "bitbucketUuid", Value: 1}},
			name:       "uniq_bitbucket_uuid",
		},
		{
			collection: "repos",
			keys:       bson.D{{Key: "workspace", Value: 1}, {Key: "repoSlug", Value: 1}},
			name:       "uniq_workspace_repo_slug",
		},
		{
			collection: "repoSubscriptions",
			keys:       bson.D{{Key: "userId", Value: 1}, {Key: "repoId", Value: 1}},
			name:       "uniq_user_repo",
		},
		{
			collection: "commits",
			keys:       bson.D{{Key: "repoId", Value: 1}, {Key: "commitHash", Value: 1}},
			name:       "uniq_repo_commit_hash",
		},
	}

	for _, spec := range specs {
		coll := database.Collection(spec.collection)
		_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    spec.keys,
			Options: options.Index().SetUnique(true).SetName(spec.name),
		})
		if err != nil {
			return fmt.Errorf("creating index %s on %s: %w", spec.name, spec.collection, err)
		}
		log.Printf("ensured index %s on %s", spec.name, spec.collection)
	}

	return nil
}
