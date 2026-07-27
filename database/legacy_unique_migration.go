package database

import (
	"fmt"
	"strings"
	"x-ui/database/model"

	"gorm.io/gorm"
)

const (
	inboundPortMigrationTable = "inbounds_port_migration"
	tunnelPortMigrationTable  = "tunnels_port_migration"
)

type inboundPortMigration model.Inbound

func (inboundPortMigration) TableName() string {
	return inboundPortMigrationTable
}

type tunnelPortMigration model.Tunnel

func (tunnelPortMigration) TableName() string {
	return tunnelPortMigrationTable
}

type sqliteIndexListRow struct {
	Seq     int    `gorm:"column:seq"`
	Name    string `gorm:"column:name"`
	Unique  int    `gorm:"column:unique"`
	Origin  string `gorm:"column:origin"`
	Partial int    `gorm:"column:partial"`
}

type sqliteIndexInfoRow struct {
	SeqNo int    `gorm:"column:seqno"`
	CID   int    `gorm:"column:cid"`
	Name  string `gorm:"column:name"`
}

type sqliteTableInfoRow struct {
	CID          int         `gorm:"column:cid"`
	Name         string      `gorm:"column:name"`
	Type         string      `gorm:"column:type"`
	NotNull      int         `gorm:"column:notnull"`
	DefaultValue interface{} `gorm:"column:dflt_value"`
	PrimaryKey   int         `gorm:"column:pk"`
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func hasSingleColumnUniqueIndex(tx *gorm.DB, table string, column string) (bool, error) {
	var indexes []sqliteIndexListRow
	if err := tx.Raw("PRAGMA index_list(" + quoteSQLiteIdentifier(table) + ")").Scan(&indexes).Error; err != nil {
		return false, err
	}
	for _, index := range indexes {
		if index.Unique != 1 {
			continue
		}
		var columns []sqliteIndexInfoRow
		if err := tx.Raw("PRAGMA index_info(" + quoteSQLiteIdentifier(index.Name) + ")").Scan(&columns).Error; err != nil {
			return false, err
		}
		if len(columns) == 1 && strings.EqualFold(columns[0].Name, column) {
			return true, nil
		}
	}
	return false, nil
}

func sqliteTableColumns(tx *gorm.DB, table string) ([]string, error) {
	var rows []sqliteTableInfoRow
	if err := tx.Raw("PRAGMA table_info(" + quoteSQLiteIdentifier(table) + ")").Scan(&rows).Error; err != nil {
		return nil, err
	}
	columns := make([]string, 0, len(rows))
	for _, row := range rows {
		columns = append(columns, row.Name)
	}
	return columns, nil
}

func sharedSQLiteColumns(tx *gorm.DB, sourceTable string, targetTable string) ([]string, error) {
	sourceColumns, err := sqliteTableColumns(tx, sourceTable)
	if err != nil {
		return nil, err
	}
	targetColumns, err := sqliteTableColumns(tx, targetTable)
	if err != nil {
		return nil, err
	}
	targetSet := make(map[string]struct{}, len(targetColumns))
	for _, column := range targetColumns {
		targetSet[column] = struct{}{}
	}
	shared := make([]string, 0, len(sourceColumns))
	for _, column := range sourceColumns {
		if _, ok := targetSet[column]; ok {
			shared = append(shared, column)
		}
	}
	if len(shared) == 0 {
		return nil, fmt.Errorf("no shared columns between %s and %s", sourceTable, targetTable)
	}
	return shared, nil
}

func rebuildTableWithoutSingleColumnUnique(table string, column string, temporaryModel interface{}) error {
	hasUnique, err := hasSingleColumnUniqueIndex(db, table, column)
	if err != nil || !hasUnique {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Migrator().DropTable(temporaryModel); err != nil {
			return err
		}
		if err := tx.Migrator().CreateTable(temporaryModel); err != nil {
			return err
		}

		temporaryTable := ""
		switch temporaryModel.(type) {
		case *inboundPortMigration:
			temporaryTable = inboundPortMigrationTable
		case *tunnelPortMigration:
			temporaryTable = tunnelPortMigrationTable
		default:
			return fmt.Errorf("unsupported migration table type %T", temporaryModel)
		}

		columns, err := sharedSQLiteColumns(tx, table, temporaryTable)
		if err != nil {
			return err
		}
		quotedColumns := make([]string, 0, len(columns))
		for _, name := range columns {
			quotedColumns = append(quotedColumns, quoteSQLiteIdentifier(name))
		}
		columnList := strings.Join(quotedColumns, ", ")
		copySQL := fmt.Sprintf(
			"INSERT INTO %s (%s) SELECT %s FROM %s",
			quoteSQLiteIdentifier(temporaryTable),
			columnList,
			columnList,
			quoteSQLiteIdentifier(table),
		)
		if err := tx.Exec(copySQL).Error; err != nil {
			return err
		}
		if err := tx.Exec("DROP TABLE " + quoteSQLiteIdentifier(table)).Error; err != nil {
			return err
		}
		renameSQL := fmt.Sprintf(
			"ALTER TABLE %s RENAME TO %s",
			quoteSQLiteIdentifier(temporaryTable),
			quoteSQLiteIdentifier(table),
		)
		return tx.Exec(renameSQL).Error
	})
}

func migrateLegacyInboundPortUnique() error {
	return rebuildTableWithoutSingleColumnUnique("inbounds", "port", &inboundPortMigration{})
}

func migrateLegacyTunnelPortUnique() error {
	return rebuildTableWithoutSingleColumnUnique("tunnels", "listen_port", &tunnelPortMigration{})
}
