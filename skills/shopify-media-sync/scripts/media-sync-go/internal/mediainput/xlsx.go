package mediainput

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

func ReadXLSX(filePath, sheetName string) ([][]string, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	files := map[string]*zip.File{}
	for _, file := range reader.File {
		files[file.Name] = file
	}

	sharedStrings, err := parseSharedStrings(files["xl/sharedStrings.xml"])
	if err != nil {
		return nil, err
	}
	sheets, err := parseWorkbookSheets(files["xl/workbook.xml"])
	if err != nil {
		return nil, err
	}
	rels, err := parseWorkbookRels(files["xl/_rels/workbook.xml.rels"])
	if err != nil {
		return nil, err
	}
	if len(sheets) == 0 {
		return nil, fmt.Errorf("xlsx 没有 worksheet")
	}
	var selected workbookSheet
	if sheetName == "" {
		selected = sheets[0]
	} else {
		for _, sheet := range sheets {
			if sheet.Name == sheetName {
				selected = sheet
				break
			}
		}
		if selected.Name == "" {
			return nil, fmt.Errorf("xlsx 未找到 sheet %q", sheetName)
		}
	}
	target := rels[selected.RelID]
	if target == "" {
		return nil, fmt.Errorf("xlsx workbook rel 缺少 %s", selected.RelID)
	}
	target = strings.TrimPrefix(target, "/")
	if !strings.HasPrefix(target, "xl/") {
		target = path.Join("xl", target)
	}
	return parseWorksheet(files[target], sharedStrings)
}

func parseSharedStrings(file *zip.File) ([]string, error) {
	if file == nil {
		return nil, nil
	}
	body, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	decoder := xml.NewDecoder(body)
	var out []string
	var inSI bool
	var builder strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				inSI = true
				builder.Reset()
			}
			if inSI && t.Name.Local == "t" {
				var text string
				if err := decoder.DecodeElement(&text, &t); err != nil {
					return nil, err
				}
				builder.WriteString(text)
			}
		case xml.EndElement:
			if t.Name.Local == "si" {
				out = append(out, builder.String())
				inSI = false
			}
		}
	}
	return out, nil
}

type workbookSheet struct {
	Name  string
	RelID string
}

func parseWorkbookSheets(file *zip.File) ([]workbookSheet, error) {
	if file == nil {
		return nil, fmt.Errorf("xlsx 缺少 xl/workbook.xml")
	}
	body, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	decoder := xml.NewDecoder(body)
	var sheets []workbookSheet
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		var sheet workbookSheet
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "name":
				sheet.Name = attr.Value
			case "id":
				if attr.Name.Space != "" {
					sheet.RelID = attr.Value
				}
			}
		}
		if sheet.Name != "" && sheet.RelID != "" {
			sheets = append(sheets, sheet)
		}
	}
	return sheets, nil
}

func parseWorkbookRels(file *zip.File) (map[string]string, error) {
	if file == nil {
		return nil, fmt.Errorf("xlsx 缺少 workbook rels")
	}
	body, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	decoder := xml.NewDecoder(body)
	rels := map[string]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var id, target string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Id":
				id = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if id != "" && target != "" {
			rels[id] = target
		}
	}
	return rels, nil
}

func parseWorksheet(file *zip.File, sharedStrings []string) ([][]string, error) {
	if file == nil {
		return nil, fmt.Errorf("xlsx 缺少 worksheet 文件")
	}
	body, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	decoder := xml.NewDecoder(body)
	rowMap := map[int]map[int]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "c" {
			continue
		}
		ref, cellType := "", ""
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "r":
				ref = attr.Value
			case "t":
				cellType = attr.Value
			}
		}
		rowIdx, colIdx := cellRefIndexes(ref)
		value, err := decodeCell(decoder, start, cellType, sharedStrings)
		if err != nil {
			return nil, err
		}
		if rowIdx < 0 || colIdx < 0 {
			continue
		}
		if rowMap[rowIdx] == nil {
			rowMap[rowIdx] = map[int]string{}
		}
		rowMap[rowIdx][colIdx] = value
	}
	maxRow, maxCol := -1, -1
	for rowIdx, cols := range rowMap {
		if rowIdx > maxRow {
			maxRow = rowIdx
		}
		for colIdx := range cols {
			if colIdx > maxCol {
				maxCol = colIdx
			}
		}
	}
	if maxRow < 0 {
		return nil, nil
	}
	rows := make([][]string, maxRow+1)
	for r := 0; r <= maxRow; r++ {
		rows[r] = make([]string, maxCol+1)
		for c := 0; c <= maxCol; c++ {
			rows[r][c] = rowMap[r][c]
		}
	}
	return rows, nil
}

func decodeCell(decoder *xml.Decoder, start xml.StartElement, cellType string, sharedStrings []string) (string, error) {
	var value string
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "v" || t.Name.Local == "t" {
				var text string
				if err := decoder.DecodeElement(&text, &t); err != nil {
					return "", err
				}
				if t.Name.Local == "v" && cellType == "s" {
					idx, err := strconv.Atoi(text)
					if err == nil && idx >= 0 && idx < len(sharedStrings) {
						text = sharedStrings[idx]
					}
				}
				value = text
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return value, nil
			}
		}
	}
}

func cellRefIndexes(ref string) (int, int) {
	if ref == "" {
		return -1, -1
	}
	col := 0
	i := 0
	for ; i < len(ref); i++ {
		ch := ref[i]
		if ch < 'A' || ch > 'Z' {
			break
		}
		col = col*26 + int(ch-'A'+1)
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil || row <= 0 || col <= 0 {
		return -1, -1
	}
	return row - 1, col - 1
}
