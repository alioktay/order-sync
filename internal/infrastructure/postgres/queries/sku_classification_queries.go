package queries

const HasHardwareSKU = `SELECT EXISTS (SELECT 1 FROM sku_classifications WHERE category = 'HARDWARE' AND sku = ANY($1::text[]))`
