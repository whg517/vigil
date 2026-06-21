package schema

import (
	"database/sql/driver"
	"fmt"

	"github.com/pgvector/pgvector-go"
)

// NullableVector 包装 pgvector.Vector，使其能正确处理 SQL NULL。
//
// 背景：pgvector.Vector.Scan 对 nil（SQL NULL）返回 "unsupported data type: <nil>"，
// 导致 Optional 的 embedding 列在读取 NULL 行时扫描失败（postgres 与 sqlite 均受影响）。
// 本类型实现 sql.Scanner + driver.Valuer：
//   - Scan(nil) → 视为空向量（Valid=false），不报错；
//   - Scan(非空) → 委托 pgvector.Vector.Parse；
//   - Value → 空时返回 nil（写回 SQL NULL），非空返回 pgvector 文本表示。
//
// 仅用于 ent schema 字段定义；业务层读写仍走 pgvector.Vector（ent 生成的实体字段类型）。
type NullableVector struct {
	pgvector.Vector
	Valid bool
}

// Scan 实现 sql.Scanner。
func (n *NullableVector) Scan(src interface{}) error {
	if src == nil {
		n.Valid = false
		return nil
	}
	if err := n.Vector.Scan(src); err != nil {
		return err
	}
	n.Valid = true
	return nil
}

// Value 实现 driver.Valuer。
func (n NullableVector) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Vector.Value()
}

// AsNullableVector 把 *pgvector.Vector（ent 实体字段）转 NullableVector，便于写回。
func AsNullableVector(v *pgvector.Vector) NullableVector {
	if v == nil {
		return NullableVector{Valid: false}
	}
	return NullableVector{Vector: *v, Valid: true}
}

// String 便于日志/调试。
func (n NullableVector) String() string {
	if !n.Valid {
		return "<null>"
	}
	return fmt.Sprintf("%v", n.Vector.String())
}
