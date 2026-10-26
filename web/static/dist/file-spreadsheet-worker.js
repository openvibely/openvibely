importScripts('/static/vendor/xlsx.full.min.js');

const MAX_ROWS = 10001;
const MAX_COLUMNS = 100;
const MAX_SHEET_CELLS = 100000;
const MAX_WORKBOOK_CELLS = 200000;
const MAX_CSV_ROWS = MAX_ROWS;
let source;
let format;
let sheetNames = [];
const dimensions = new Map();

function dimensionsFor(sheet) {
  const ref = sheet && (sheet['!fullref'] || sheet['!ref']);
  if (!ref) return {totalRows: 0, totalColumns: 0};
  const end = XLSX.utils.decode_range(ref).e;
  return {totalRows: end.r + 1, totalColumns: end.c + 1};
}

function readWorkbook(options) {
  return XLSX.read(source, {
    type: 'array',
    bookVBA: false,
    cellHTML: false,
    ...options
  });
}

function rememberDimensions(workbook, indexes) {
  for (const index of indexes) {
    const name = sheetNames[index];
    dimensions.set(index, dimensionsFor(workbook.Sheets && workbook.Sheets[name]));
  }
}

function sheetAllocation(index) {
  let remainingCells = MAX_WORKBOOK_CELLS;
  for (let current = 0; current <= index; current++) {
    const {totalRows, totalColumns} = dimensions.get(current) || {totalRows: 0, totalColumns: 0};
    const columns = Math.min(totalColumns, MAX_COLUMNS);
    const rowCount = columns
      ? Math.min(totalRows, MAX_ROWS, Math.floor(Math.min(MAX_SHEET_CELLS, remainingCells) / columns))
      : 0;
    if (current === index) return {columns, rowCount, totalRows, totalColumns};
    remainingCells -= rowCount * columns;
  }
  return {columns: 0, rowCount: 0, totalRows: 0, totalColumns: 0};
}

function makeSheetPreview(index, workbook) {
  const name = sheetNames[index];
  const {columns, rowCount, totalRows, totalColumns} = sheetAllocation(index);
  const sheet = workbook && workbook.Sheets && workbook.Sheets[name];
  const rows = rowCount && columns
    ? XLSX.utils.sheet_to_json(sheet || {}, {
        header: 1,
        blankrows: true,
        range: {s: {r: 0, c: 0}, e: {r: rowCount - 1, c: columns - 1}},
        defval: '',
        raw: false
      }).map(row => row.map(value => String(value)))
    : [];
  return {
    name,
    rows,
    totalRows,
    totalColumns,
    truncated: totalRows > rowCount || totalColumns > columns
  };
}

function csvDelimiter(text, requestedFormat) {
  if (requestedFormat === 'tsv') return '\t';
  const counts = {'\t': 0, ',': 0, ';': 0, '|': 0};
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (char === '"') {
      if (quoted && text[i + 1] === '"') i++;
      else quoted = !quoted;
    } else if (!quoted && (char === '\n' || char === '\r')) {
      break;
    } else if (!quoted && Object.prototype.hasOwnProperty.call(counts, char)) {
      counts[char]++;
    }
  }
  return Object.keys(counts).reduce((best, candidate) => counts[candidate] > counts[best] ? candidate : best, ',');
}

function scanDelimited(text, delimiter, rowLimit, columnLimit) {
  const previewRows = rowLimit ? [] : null;
  let totalRows = 0;
  let totalColumns = 0;
  let row = [];
  let field = '';
  let rowIndex = 0;
  let columnIndex = 0;
  let quoted = false;
  let recordStarted = false;

  const append = value => {
    if (rowLimit && rowIndex < rowLimit && columnIndex < columnLimit) field += value;
    if (value) recordStarted = true;
  };
  const finishField = () => {
    if (rowLimit && rowIndex < rowLimit && columnIndex < columnLimit) row[columnIndex] = field;
    field = '';
    columnIndex++;
    recordStarted = true;
  };
  const finishRow = () => {
    finishField();
    totalRows = Math.max(totalRows, rowIndex + 1);
    totalColumns = Math.max(totalColumns, columnIndex);
    if (previewRows && rowIndex < rowLimit) previewRows[rowIndex] = row;
    row = [];
    rowIndex++;
    columnIndex = 0;
    recordStarted = false;
  };

  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (quoted) {
      if (char === '"' && text[i + 1] === '"') {
        append('"');
        i++;
      } else if (char === '"') {
        quoted = false;
      } else {
        append(char);
      }
    } else if (char === '"') {
      quoted = true;
      recordStarted = true;
    } else if (char === delimiter) {
      finishField();
    } else if (char === '\n' || char === '\r') {
      finishRow();
      if (char === '\r' && text[i + 1] === '\n') i++;
    } else {
      append(char);
    }
  }
  if (recordStarted || columnIndex > 0 || field.length > 0 || totalRows === 0) finishRow();
  return {totalRows, totalColumns, rows: previewRows};
}

function readDelimited(buffer, requestedFormat) {
  let text = new TextDecoder('utf-8').decode(buffer);
  if (text.charCodeAt(0) === 0xFEFF) text = text.slice(1);
  const delimiter = csvDelimiter(text, requestedFormat);
  const dimensions = scanDelimited(text, delimiter, 0, 0);
  const columns = Math.min(dimensions.totalColumns, MAX_COLUMNS);
  const rowCount = columns
    ? Math.min(dimensions.totalRows, MAX_ROWS, Math.floor(Math.min(MAX_SHEET_CELLS, MAX_WORKBOOK_CELLS) / columns))
    : 0;
  const preview = scanDelimited(text, delimiter, rowCount, columns);
  const rows = (preview.rows || []).map(values =>
    Array.from({length: columns}, (_, index) => String(values[index] || ''))
  );
  return {
    name: 'Sheet1',
    rows,
    totalRows: dimensions.totalRows,
    totalColumns: dimensions.totalColumns,
    truncated: dimensions.totalRows > rowCount || dimensions.totalColumns > columns
  };
}

function loadSheet(index, requestId, initialProbe) {
  try {
    if (!Number.isInteger(index) || index < 0 || index >= sheetNames.length) {
      throw new Error('Worksheet is unavailable');
    }
    const missing = [];
    for (let current = 0; current <= index; current++) {
      if (!dimensions.has(current)) missing.push(current);
    }
    let probe = initialProbe;
    if (missing.length) {
      probe = readWorkbook({sheets: missing, sheetRows: 1});
      rememberDimensions(probe, missing);
    }
    const allocation = sheetAllocation(index);
    if (allocation.rowCount <= 1 && !probe) {
      probe = readWorkbook({sheets: [index], sheetRows: 1});
    }
    let workbook = allocation.rowCount <= 1 ? probe : null;
    if (allocation.rowCount > 1) {
      workbook = readWorkbook({sheets: [index], sheetRows: allocation.rowCount});
    }
    const preview = makeSheetPreview(index, workbook);
    postMessage({type: requestId == null ? 'ready' : 'sheet', names: sheetNames, index, sheet: preview, requestId});
  } catch (error) {
    postMessage({type: 'error', error: 'Could not read this spreadsheet: ' + (error.message || error), requestId});
    if (requestId == null) close();
  }
}

onmessage = event => {
  const message = event.data;
  if (message && message.type === 'load') {
    loadSheet(message.index, message.requestId);
    return;
  }
  if (message && message.type === 'dispose') {
    source = null;
    close();
    return;
  }
  try {
    source = message && message.buffer && Object.prototype.toString.call(message.buffer) === '[object ArrayBuffer]' ? message.buffer : message;
    format = message && message.format;
    if (format === 'csv' || format === 'tsv') {
      const sheet = readDelimited(source, format);
      source = null;
      sheetNames = [sheet.name];
      postMessage({type: 'ready', names: sheetNames, index: 0, sheet});
      return;
    }
    const catalog = readWorkbook({bookSheets: true});
    sheetNames = catalog.SheetNames || [];
    if (!sheetNames.length) {
      postMessage({type: 'ready', names: [], index: -1, sheet: null});
      return;
    }
    const workbook = readWorkbook({sheets: 0, sheetRows: 1});
    rememberDimensions(workbook, [0]);
    loadSheet(0, null, workbook);
  } catch (error) {
    postMessage({type: 'error', error: 'Could not read this spreadsheet: ' + (error.message || error)});
    close();
  }
};
