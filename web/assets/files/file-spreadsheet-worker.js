importScripts('/static/vendor/xlsx.full.min.js');
onmessage = event => {
  try {
    const bytes = new Uint8Array(event.data);
    // Binary workbooks (ZIP/OLE containers, UTF-16 text) contain NUL bytes or fail strict UTF-8
    // decoding. Other input is plain-text CSV/TSV, which must be decoded as UTF-8 explicitly
    // because SheetJS otherwise treats the bytes as a legacy single-byte codepage.
    let text = null;
    if (bytes.length && !bytes.includes(0)) {
      try { text = new TextDecoder('utf-8', {fatal: true}).decode(bytes); } catch (_) { text = null; }
    }
    const workbook = text !== null
      ? XLSX.read(text, {type: 'string', bookVBA: false, cellHTML: false})
      : XLSX.read(event.data, {type: 'array', bookVBA: false, cellHTML: false});
    const maxRows = 10001, maxColumns = 100, maxSheetCells = 100000;
    let remainingCells = 200000;
    const sheets = workbook.SheetNames.map(name => {
      const sheet = workbook.Sheets[name];
      const end = XLSX.utils.decode_range(sheet['!ref'] || 'A1').e;
      const totalRows = sheet['!ref'] ? end.r + 1 : 0;
      const totalColumns = totalRows ? end.c + 1 : 0;
      // Bound both dimensions and the workbook's total allocation before expanding
      // sparse cells. Keep coordinates anchored at A1, including blank rows.
      const columns = Math.min(totalColumns, maxColumns);
      const rowCount = columns ? Math.min(totalRows, maxRows, Math.floor(Math.min(maxSheetCells, remainingCells) / columns)) : 0;
      const range = {s: {r: 0, c: 0}, e: {r: rowCount - 1, c: columns - 1}};
      const rows = rowCount ? XLSX.utils.sheet_to_json(sheet, {
        header: 1, blankrows: true, range, defval: '', raw: false
      }).map(row => row.map(value => String(value))) : [];
      remainingCells -= rowCount * columns;
      return {name, rows, totalRows, totalColumns, truncated: totalRows > rowCount || totalColumns > columns};
    });
    postMessage({sheets});
  } catch (error) {
    postMessage({error: 'Could not read this spreadsheet: ' + error.message});
  }
  close();
};
