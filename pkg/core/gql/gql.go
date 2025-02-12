package gql

import (
	"github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_gql"
)

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require here.

var _ core_gql.ResolverRoot = &GraphQLServer{}

type GraphQLServer struct{
	config *config.Config
	logger *common.Logger
	db *db.Queries
}

func NewGraphQLServer(config *config.Config, logger *common.Logger, db *db.Queries) *GraphQLServer{
	return &GraphQLServer{
		config: config,
		logger: logger,
		db: db,
	}
}
