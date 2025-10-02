// implementation of the grpc service definition found in the audius protocol.proto spec
package server

import (
	"reflect"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/iancoleman/strcase"
	"google.golang.org/protobuf/proto"
)

var (
	TrackPlaysProtoName     string
	ManageEntitiesProtoName string
	SlaRollupProtoName      string
	SlaNodeReportProtoName  string
)

func init() {
	TrackPlaysProtoName = GetProtoTypeName(&corev1.TrackPlays{})
	ManageEntitiesProtoName = GetProtoTypeName(&corev1.ManageEntityLegacy{})
	SlaRollupProtoName = GetProtoTypeName(&corev1.SlaRollup{})
	SlaNodeReportProtoName = GetProtoTypeName(&corev1.SlaNodeReport{})
}

func GetProtoTypeName(msg proto.Message) string {
	return strcase.ToSnake(reflect.TypeOf(msg).Elem().Name())
}
