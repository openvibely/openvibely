importScripts('/static/vendor/xlsx.full.min.js');

const MAX_ROWS = 10001;
const MAX_COLUMNS = 100;
const MAX_SHEET_CELLS = 100000;
const MAX_WORKBOOK_CELLS = 200000;
const MAX_CSV_ROWS = MAX_ROWS;
const MAX_DELIMITER_SAMPLE_BYTES = 64 * 1024;
const MAX_ODS_SCAN_CHARS = 4096;
const MAX_SHARED_STRING_CACHE_BYTES = 8 * 1024 * 1024;
const MAX_SHARED_STRING_CACHE_ENTRIES = 50000;
let source;
let format;
let sharedStringsURL;
let sheetNames = [];
let workbookMetadata;
let worksheetPathsPromise;
const dimensions = new Map();
const sharedStringsCache = new Map();
let sharedStringsCacheBytes = 0;

function dimensionsFor(sheet) {
  const ref = sheet && (sheet['!fullref'] || sheet['!ref']);
  if (!ref) return {totalRows: 0, totalColumns: 0};
  const end = XLSX.utils.decode_range(ref).e;
  return {totalRows: end.r + 1, totalColumns: end.c + 1};
}

function readWorkbook(options) {
  return XLSX.read(source, {
    type: 'array',
    lazyZipEntries: true,
    deferSSTResolution: ['xlsx','xlsm','xltx','xltm'].includes(format),
    bookVBA: false,
    cellHTML: false,
    sheetCols: MAX_COLUMNS,
    ...options
  });
}


function zipEntryInfo(buffer, path) {
  const bytes = new Uint8Array(buffer);
  const view = new DataView(buffer);
  let eocd = -1;
  for (let offset = bytes.length - 22, min = Math.max(0, bytes.length - 65557); offset >= min; offset--) {
    if (view.getUint32(offset, true) === 0x06054b50) { eocd = offset; break; }
  }
  if (eocd < 0) throw new Error('Spreadsheet ZIP directory is unavailable');
  const entries = view.getUint16(eocd + 10, true);
  let offset = view.getUint32(eocd + 16, true);
  const decoder = new TextDecoder('utf-8');
  for (let index = 0; index < entries; index++) {
    if (view.getUint32(offset, true) !== 0x02014b50) throw new Error('Invalid spreadsheet ZIP directory');
    const method = view.getUint16(offset + 10, true);
    const compressedSize = view.getUint32(offset + 20, true);
    const nameLength = view.getUint16(offset + 28, true);
    const extraLength = view.getUint16(offset + 30, true);
    const commentLength = view.getUint16(offset + 32, true);
    const localOffset = view.getUint32(offset + 42, true);
    const name = decoder.decode(bytes.subarray(offset + 46, offset + 46 + nameLength)).replace(/\\/g, '/');
    if (name.toLowerCase() === path.toLowerCase()) {
      if (view.getUint32(localOffset, true) !== 0x04034b50) throw new Error('Invalid spreadsheet ZIP entry');
      const dataOffset = localOffset + 30 + view.getUint16(localOffset + 26, true) + view.getUint16(localOffset + 28, true);
      return {method, compressedSize, dataOffset};
    }
    offset += 46 + nameLength + extraLength + commentLength;
  }
  return null;
}

// ODS has a single potentially enormous XML member. Decode raw DEFLATE with
// fixed output/history buffers instead of depending on native output chunk sizes.
function boundedInflateStream(compressed) {
  let position = 0, bits = 0, bitCount = 0, historyPosition = 0, historySize = 0;
  const history = new Uint8Array(32768);
  let state = 'header', finalBlock = false, stored = 0, matchLength = 0, matchDistance = 0;
  let literals, distances;
  const readBits = count => {
    while (bitCount < count) {
      if (position >= compressed.length) throw new Error('Truncated spreadsheet DEFLATE stream');
      bits |= compressed[position++] << bitCount;
      bitCount += 8;
    }
    const value = bits & ((1 << count) - 1);
    bits >>>= count;
    bitCount -= count;
    return value;
  };
  const huffman = lengths => {
    const counts = new Uint16Array(16), next = new Uint16Array(16);
    let maximum = 0, available = 1, code = 0;
    for (const length of lengths) { counts[length]++; maximum = Math.max(maximum, length); }
    counts[0] = 0;
    for (let length = 1; length <= 15; length++) {
      available = available * 2 - counts[length];
      if (available < 0) throw new Error('Invalid spreadsheet Huffman tree');
      code = (code + counts[length - 1]) << 1;
      next[length] = code;
    }
    const table = new Uint16Array(1 << (maximum + 1));
    lengths.forEach((length, symbol) => {
      if (!length) return;
      let value = next[length]++, reversed = 0;
      for (let bit = 0; bit < length; bit++) { reversed = reversed * 2 + (value & 1); value >>>= 1; }
      table[(1 << length) | reversed] = symbol + 1;
    });
    return {table, maximum};
  };
  const symbol = tree => {
    let code = 0;
    for (let length = 1; length <= tree.maximum; length++) {
      code |= readBits(1) << (length - 1);
      const value = tree.table[(1 << length) | code];
      if (value) return value - 1;
    }
    throw new Error('Invalid spreadsheet Huffman code');
  };
  const lengthBases = [3,4,5,6,7,8,9,10,11,13,15,17,19,23,27,31,35,43,51,59,67,83,99,115,131,163,195,227,258];
  const lengthBits = [0,0,0,0,0,0,0,0,1,1,1,1,2,2,2,2,3,3,3,3,4,4,4,4,5,5,5,5,0];
  const distanceBases = [1,2,3,4,5,7,9,13,17,25,33,49,65,97,129,193,257,385,513,769,1025,1537,2049,3073,4097,6145,8193,12289,16385,24577];
  const distanceBits = [0,0,0,0,1,1,2,2,3,3,4,4,5,5,6,6,7,7,8,8,9,9,10,10,11,11,12,12,13,13];
  const finishBlock = () => { state = finalBlock ? 'done' : 'header'; };
  return new ReadableStream({
    pull(controller) {
      const output = new Uint8Array(MAX_ODS_SCAN_CHARS);
      let used = 0;
      const emit = value => {
        output[used++] = value;
        history[historyPosition] = value;
        historyPosition = (historyPosition + 1) & 32767;
        historySize = Math.min(32768, historySize + 1);
      };
      while (used < output.length && state !== 'done') {
        if (matchLength) {
          emit(history[(historyPosition - matchDistance + 32768) & 32767]);
          matchLength--;
        } else if (state === 'stored') {
          if (stored) { emit(readBits(8)); stored--; }
          else finishBlock();
        } else if (state === 'header') {
          finalBlock = !!readBits(1);
          const type = readBits(2);
          if (type === 0) {
            readBits(bitCount % 8);
            stored = readBits(16);
            if ((stored ^ readBits(16)) !== 65535) throw new Error('Invalid spreadsheet stored block');
            state = 'stored';
            continue;
          }
          if (type === 1) {
            literals = huffman(Array.from({length:288}, (_, i) => i < 144 ? 8 : i < 256 ? 9 : i < 280 ? 7 : 8));
            distances = huffman(Array(32).fill(5));
          } else if (type === 2) {
            const literalCount = readBits(5) + 257, distanceCount = readBits(5) + 1, codeCount = readBits(4) + 4;
            if (literalCount > 286) throw new Error('Invalid spreadsheet literal count');
            // Code-length alphabet order from the DEFLATE format.
            const codeLengths = Array(19).fill(0);
            const codeOrder = [16,17,18,0,8,7,9,6,10,5,11,4,12,3,13,2,14,1,15];
            for (let i = 0; i < codeCount; i++) codeLengths[codeOrder[i]] = readBits(3);
            const codes = huffman(codeLengths), lengths = [];
            while (lengths.length < literalCount + distanceCount) {
              const value = symbol(codes);
              if (value < 16) lengths.push(value);
              else {
                if (value === 16 && !lengths.length) throw new Error('Invalid spreadsheet length repeat');
                const count = value === 16 ? readBits(2) + 3 : value === 17 ? readBits(3) + 3 : readBits(7) + 11;
                const previous = value === 16 ? lengths[lengths.length - 1] : 0;
                if (lengths.length + count > literalCount + distanceCount) throw new Error('Invalid spreadsheet length count');
                for (let i = 0; i < count; i++) lengths.push(previous);
              }
            }
            if (!lengths[256]) throw new Error('Missing spreadsheet end-of-block code');
            literals = huffman(lengths.slice(0, literalCount));
            distances = huffman(lengths.slice(literalCount));
          } else throw new Error('Invalid spreadsheet DEFLATE block');
          state = 'codes';
        } else {
          const value = symbol(literals);
          if (value < 256) emit(value);
          else if (value === 256) finishBlock();
          else {
            if (value > 285) throw new Error('Invalid spreadsheet length symbol');
            matchLength = lengthBases[value - 257] + readBits(lengthBits[value - 257]);
            const distance = symbol(distances);
            if (distance > 29) throw new Error('Invalid spreadsheet distance symbol');
            matchDistance = distanceBases[distance] + readBits(distanceBits[distance]);
            if (matchDistance > historySize) throw new Error('Invalid spreadsheet back-reference');
          }
        }
      }
      if (used) controller.enqueue(output.subarray(0, used));
      if (state === 'done') controller.close();
    }
  }, {highWaterMark:0});
}

function zipEntryStream(buffer, path, boundedOutput = false) {
  const info = zipEntryInfo(buffer, path);
  if (!info) return null;
  const bytes = new Uint8Array(buffer);
  const compressed = bytes.subarray(info.dataOffset, info.dataOffset + info.compressedSize);
  let inputOffset = 0;
  const input = new ReadableStream({
    pull(controller) {
      if (inputOffset >= compressed.length) {
        controller.close();
        return;
      }
      const end = Math.min(inputOffset + 4096, compressed.length);
      controller.enqueue(compressed.subarray(inputOffset, end));
      inputOffset = end;
    }
  });
  if (info.method === 0) return input;
  if (info.method === 8) return boundedOutput ? boundedInflateStream(compressed) : input.pipeThrough(new DecompressionStream('deflate-raw'));
  throw new Error('Unsupported spreadsheet ZIP compression method ' + info.method);
}

function isZipBuffer(buffer) {
  return Object.prototype.toString.call(buffer) === '[object ArrayBuffer]' && buffer.byteLength >= 4
    && new DataView(buffer).getUint32(0, true) === 0x04034b50;
}

function xmlTagEnd(text, start) {
  let quote = '';
  for (let index = start; index < text.length; index++) {
    const char = text[index];
    if (quote) {
      if (char === quote) quote = '';
    } else if (char === '"' || char === "'") {
      quote = char;
    } else if (char === '>') {
      return index;
    }
  }
  return -1;
}

function xmlUnescape(value) {
  return value.replace(/&(#x[0-9a-f]+|#\d+|amp|lt|gt|quot|apos);/gi, (match, entity) => {
    if (entity[0] === '#') {
      const code = entity[1].toLowerCase() === 'x' ? parseInt(entity.slice(2), 16) : Number(entity.slice(1));
      return Number.isFinite(code) ? String.fromCodePoint(code) : match;
    }
    return {amp: '&', lt: '<', gt: '>', quot: '"', apos: "'"}[entity.toLowerCase()] || match;
  });
}

function makeODSDocument(rootOpen, preamble, tableXML) {
  const root = rootOpen || '<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" xmlns:number="urn:oasis:names:tc:opendocument:xmlns:datastyle:1.0" office:version="1.2">';
  const match = root.match(/^<((?:[\w.-]+:)?document-content)\b/i);
  const documentTag = match ? match[1].replace(/document-content$/i, 'document') : 'office:document';
  const fodsRoot = root.replace(/document-content(?=[\s>])/i, 'document');
  return '<?xml version="1.0" encoding="UTF-8"?>' + fodsRoot + (preamble || '') + '<office:body><office:spreadsheet>' + tableXML + '</office:spreadsheet></office:body></' + documentTag + '>';
}

function findODSClosingTable(text) {
  const lower = text.toLowerCase();
  let at = lower.indexOf('</table:table');
  while (at >= 0) {
    const boundary = lower[at + '</table:table'.length];
    if (boundary && /[\s>]/.test(boundary)) return at;
    at = lower.indexOf('</table:table', at + 1);
  }
  return -1;
}

function odsRepeat(attributes, name) {
  const count = Number(attributes[name] || 1);
  return Number.isFinite(count) && count > 0 ? Math.floor(count) : 1;
}

function odsSetRepeat(tag, name, count) {
  const doubleQuoted = new RegExp('(' + name + '\\s*=\\s*")\\d+(")');
  if (doubleQuoted.test(tag)) return tag.replace(doubleQuoted, '$1' + count + '$2');
  const singleQuoted = new RegExp("(" + name + "\\s*=\\s*')\\d+(')");
  if (singleQuoted.test(tag)) return tag.replace(singleQuoted, '$1' + count + '$2');
  if (count <= 1) return tag;
  return tag.replace(/\s*\/>$/, ' ' + name + '="' + count + '"/>').replace(/>$/, ' ' + name + '="' + count + '">');
}

function odsRowsForPreview(totalColumns, capacity) {
  const columns = Math.min(Math.max(totalColumns, 1), MAX_COLUMNS);
  return Math.min(MAX_ROWS, Math.floor(Math.min(MAX_SHEET_CELLS, capacity) / columns));
}

function resizeODSCapture(active) {
  const rowLimit = odsRowsForPreview(active.totalColumns, active.capacity);
  const columns = Math.min(Math.max(active.totalColumns, 1), MAX_COLUMNS);
  if (rowLimit === active.rowLimit && columns === active.previewColumns) return;
  active.rowLimit = rowLimit;
  active.previewColumns = columns;
  active.rows = active.rows.filter(row => row.index < rowLimit);
  if (active.row && active.row.index >= rowLimit) {
    active.row.capture = false;
    active.row.parts = [];
  }
}

async function consumeODSTable(active, text, onRowProgress) {
  active.tokenCarry += text;
  const maybeNotifyProgress = async () => {
    if (onRowProgress && active.capture && !active.previewSent && active.rowLimit > 0 && active.capturedRows >= active.rowLimit) {
      await onRowProgress();
      await new Promise(resolve => setTimeout(resolve, 0));
    }
  };
  let offset = 0;
  while (offset < active.tokenCarry.length) {
    const open = active.tokenCarry.indexOf('<', offset);
    if (open < 0) {
      if (active.cell && active.cell.capture) active.cell.parts.push(active.tokenCarry.slice(offset));
      active.tokenCarry = '';
      return;
    }
    if (active.cell && active.cell.capture && open > offset) active.cell.parts.push(active.tokenCarry.slice(offset, open));
    const end = xmlTagEnd(active.tokenCarry, open);
    if (end < 0) {
      // Text preceding this incomplete tag was already captured above.
      active.tokenCarry = active.tokenCarry.slice(open);
      return;
    }
    const tag = active.tokenCarry.slice(open, end + 1);
    offset = end + 1;
    if (tag.startsWith('<!--') || tag.startsWith('<?') || tag.startsWith('<!')) {
      if (active.cell && active.cell.capture) active.cell.parts.push(tag);
      continue;
    }
    const match = /^<\/?(?:[\w.-]+:)?([\w.-]+)/.exec(tag);
    if (!match) continue;
    const name = match[1].toLowerCase();
    const closing = /^<\//.test(tag);
    const selfClosing = /\/\s*>$/.test(tag);
    if (active.cell) {
      if (closing && name === active.cell.name) {
        if (active.cell.capture && active.row && active.row.capture) active.row.parts.push(active.cell.parts.join('') + tag);
        active.cell = null;
      } else if (active.cell.capture) active.cell.parts.push(tag);
      continue;
    }
    if (!closing && name === 'table-row') {
      const attributes = xmlAttributes(tag);
      const repeat = odsRepeat(attributes, 'table:number-rows-repeated');
      const index = active.totalRows;
      active.totalRows = Math.min(Number.MAX_SAFE_INTEGER, active.totalRows + repeat);
      const rowLimit = odsRowsForPreview(active.totalColumns, active.capacity);
      const visibleRepeat = Math.max(0, Math.min(repeat, rowLimit - index));
      const capture = index < rowLimit && active.capacity > 0;
      const opening = capture && visibleRepeat < repeat ? odsSetRepeat(tag, 'table:number-rows-repeated', visibleRepeat) : tag;
      active.row = {index, repeat, capture, column:0, parts:capture ? [opening] : []};
      active.totalColumns = Math.max(active.totalColumns, 0);
      resizeODSCapture(active);
      if (selfClosing) {
        if (active.row.capture) {
          active.rows.push({index,xml:active.row.parts.join('')});
          active.capturedRows = Math.max(active.capturedRows, index + active.row.repeat);
        }
        active.row = null;
        await maybeNotifyProgress();
      }
      continue;
    }
    if (closing && name === 'table-row') {
      if (active.row) {
        if (active.row.capture) {
          active.row.parts.push(tag);
          active.rows.push({index:active.row.index,xml:active.row.parts.join('')});
          active.capturedRows = Math.max(active.capturedRows, active.row.index + active.row.repeat);
        }
        active.row = null;
        await maybeNotifyProgress();
      }
      continue;
    }
    if (active.row && !closing && (name === 'table-cell' || name === 'covered-table-cell')) {
      const attributes = xmlAttributes(tag);
      const repeat = odsRepeat(attributes, 'table:number-columns-repeated');
      const column = active.row.column || 0;
      active.row.column = column + repeat;
      active.totalColumns = Math.max(active.totalColumns, active.row.column);
      resizeODSCapture(active);
      const visibleColumns = Math.max(0, Math.min(repeat, MAX_COLUMNS - column));
      const capture = active.row.capture && active.row.index < active.rowLimit && column < MAX_COLUMNS && visibleColumns > 0;
      const opening = capture && visibleColumns < repeat ? odsSetRepeat(tag, 'table:number-columns-repeated', visibleColumns) : tag;
      active.cell = {name, capture, parts:capture ? [opening] : []};
      if (selfClosing) {
        if (capture && active.row.capture) active.row.parts.push(active.cell.parts.join(''));
        active.cell = null;
      }
      continue;
    }
    if (active.row && active.row.capture && !selfClosing && !closing && name !== 'table-row') active.row.parts.push(tag);
    else if (active.row && active.row.capture && closing && name !== 'table-row') active.row.parts.push(tag);
  }
  active.tokenCarry = active.tokenCarry.slice(offset);
}

async function streamODSTables(buffer, targetIndex, onTable, onCatalog, stopAtTarget, onProgress) {
  const stream = zipEntryStream(buffer, 'content.xml', true);
  if (!stream) throw new Error('OpenDocument content.xml is unavailable');
  const reader = stream.getReader();
  const decoder = new TextDecoder('utf-8');
  const openingMarker = '<table:table';
  const closingMarker = '</table:table';
  let pending = '';
  let rootOpen = '';
  let preamble = '';
  let preambleDone = false;
  let active = null;
  let names = [];
  let reachedTarget = false;
  let stopped = false;
  let bytesSinceYield = 0;
  const notifyProgress = async () => {
    const current = active;
    const requiredRows = current ? Math.max(1, current.rowLimit) : 0;
    if (!onProgress || !current || current.index !== targetIndex || current.previewSent || current.rowLimit <= 0 || current.capturedRows < requiredRows) return;
    current.previewSent = true;
    const tableXML = current.capture ? current.opening + current.rows.map(row => row.xml).join('') + '</table:table>' : null;
    await onProgress(current.index, current.name, tableXML, rootOpen, preamble, {totalRows:current.totalRows,totalColumns:current.totalColumns,dimensionsPending:true});
  };

  const process = async text => {
    pending += text;
    while (true) {
      if (active && active.skipBody) {
        const scanLength = Math.min(pending.length, MAX_ODS_SCAN_CHARS + closingMarker.length - 1);
        const closeAt = findODSClosingTable(pending.slice(0, scanLength));
        if (closeAt < 0) {
          const safeLength = Math.min(MAX_ODS_SCAN_CHARS, Math.max(0, pending.length - closingMarker.length + 1));
          if (!safeLength) return;
          pending = pending.slice(safeLength);
          await new Promise(resolve => setTimeout(resolve, 0));
          continue;
        }
        const end = xmlTagEnd(pending, closeAt);
        if (end < 0) { pending = pending.slice(closeAt); return; }
        pending = pending.slice(end + 1);
        names.push(active.name);
        active = null;
        continue;
      }

      if (active) {
        const scanLength = Math.min(pending.length, MAX_ODS_SCAN_CHARS + closingMarker.length - 1);
        const closeAt = findODSClosingTable(pending.slice(0, scanLength));
        if (closeAt < 0) {
          const safeLength = Math.min(MAX_ODS_SCAN_CHARS, Math.max(0, pending.length - closingMarker.length + 1));
          if (!safeLength) return;
          await consumeODSTable(active, pending.slice(0, safeLength), notifyProgress);
          await notifyProgress();
          pending = pending.slice(safeLength);
          continue;
        }
        const end = xmlTagEnd(pending, closeAt);
        if (end < 0) {
          await consumeODSTable(active, pending.slice(0, closeAt), notifyProgress);
          pending = pending.slice(closeAt);
          return;
        }
        await consumeODSTable(active, pending.slice(0, closeAt), notifyProgress);
        await notifyProgress();
        const closing = pending.slice(closeAt, end + 1);
        pending = pending.slice(end + 1);
        const completed = active;
        active = null;
        names.push(completed.name);
        if (completed.index <= targetIndex) {
          const tableXML = completed.capture
            ? completed.opening + completed.rows.map(row => row.xml).join('') + closing
            : null;
          await onTable(completed.index, completed.name, tableXML, rootOpen, preamble, {
            totalRows:completed.totalRows,
            totalColumns:completed.totalColumns
          });
        }
        if (completed.index === targetIndex) {
          reachedTarget = true;
          if (stopAtTarget) { stopped = true; return; }
        }
        continue;
      }

      if (!rootOpen) {
        const rootAt = pending.search(/<(?:[\w.-]+:)?document-content\b/i);
        if (rootAt >= 0) {
          const end = xmlTagEnd(pending, rootAt);
          if (end < 0) { pending = pending.slice(rootAt); return; }
          rootOpen = pending.slice(rootAt, end + 1);
          pending = pending.slice(end + 1);
          continue;
        }
      }

      if (rootOpen && !preambleDone) {
        const bodyAt = pending.search(/<(?:[\w.-]+:)?body\b/i);
        if (bodyAt < 0) {
          const safeLength = Math.max(0, pending.length - 16);
          preamble += pending.slice(0, safeLength);
          pending = pending.slice(safeLength);
          return;
        }
        const bodyEnd = xmlTagEnd(pending, bodyAt);
        if (bodyEnd < 0) {
          preamble += pending.slice(0, bodyAt);
          pending = pending.slice(bodyAt);
          return;
        }
        preamble += pending.slice(0, bodyAt);
        pending = pending.slice(bodyEnd + 1);
        preambleDone = true;
        continue;
      }

      const tableAt = pending.toLowerCase().indexOf(openingMarker);
      if (tableAt < 0) {
        pending = pending.slice(-openingMarker.length + 1);
        return;
      }
      const boundary = pending[tableAt + openingMarker.length];
      if (boundary && !/[\s/>]/.test(boundary)) {
        pending = pending.slice(tableAt + openingMarker.length);
        continue;
      }
      const end = xmlTagEnd(pending, tableAt);
      if (end < 0) { pending = pending.slice(tableAt); return; }
      const opening = pending.slice(tableAt, end + 1);
      const nameMatch = opening.match(/(?:table:)?name\s*=\s*(["'])(.*?)\1/i);
      const name = nameMatch ? xmlUnescape(nameMatch[2]) : 'Sheet' + (names.length + 1);
      const index = names.length;
      const capture = index === targetIndex;
      active = {
        index,name,opening,capture,
        // Earlier measured tables already have exact dimensions for allocation.
        // Only unmeasured predecessors need a dimension pass when jumping tabs.
        skipBody:!capture && (index > targetIndex || (dimensions.has(index) && !dimensions.get(index).dimensionsPending)),
        capacity:capture ? remainingCellCapacity(index) : 0,
        rows:[],row:null,cell:null,tokenCarry:'',totalRows:0,totalColumns:0,rowLimit:0,previewColumns:0,capturedRows:0,previewSent:false
      };
      pending = pending.slice(end + 1);
      if (/\/\s*>$/.test(opening)) {
        const completed = active;
        active = null;
        names.push(completed.name);
        if (completed.index <= targetIndex) await onTable(completed.index, completed.name, completed.capture ? opening : null, rootOpen, preamble, {totalRows:0,totalColumns:0});
        if (completed.index === targetIndex) {
          reachedTarget = true;
          if (stopAtTarget) { stopped = true; return; }
        }
      }
    }
  };

  try {
    while (!stopped) {
      const next = await reader.read();
      if (next.done) {
        await process(decoder.decode());
        if (!reachedTarget) throw new Error('Worksheet is unavailable');
        if (!stopAtTarget) onCatalog(names.slice());
        break;
      }
      // Native decompressors may deliver an entire table in one output chunk.
      // Never decode that chunk into a full XML string or retain its decoded
      // remainder in pending while the preview is displayed.
      for (let offset = 0; offset < next.value.byteLength && !stopped; offset += MAX_ODS_SCAN_CHARS) {
        const window = next.value.subarray(offset, offset + MAX_ODS_SCAN_CHARS);
        await process(decoder.decode(window, {stream:true}));
        bytesSinceYield += window.byteLength;
        if (bytesSinceYield >= 256 * 1024) {
          bytesSinceYield = 0;
          await new Promise(resolve => setTimeout(resolve, 0));
        }
      }
    }
  } finally {
    if (stopped || !reachedTarget) await reader.cancel();
    reader.releaseLock();
  }
  return names;
}

function sharedStringCacheSize(entry) {
  return 64 + String(entry && entry.t || '').length * 2 + String(entry && entry.r || '').length * 2;
}

function getCachedSharedString(index) {
  const entry = sharedStringsCache.get(index);
  if (!entry) return null;
  sharedStringsCache.delete(index);
  sharedStringsCache.set(index, entry);
  return entry;
}

function cacheSharedString(index, entry) {
  const size = sharedStringCacheSize(entry);
  if (size > MAX_SHARED_STRING_CACHE_BYTES) return;
  const previous = sharedStringsCache.get(index);
  if (previous) {
    sharedStringsCacheBytes -= sharedStringCacheSize(previous);
    sharedStringsCache.delete(index);
  }
  sharedStringsCache.set(index, entry);
  sharedStringsCacheBytes += size;
  while (sharedStringsCacheBytes > MAX_SHARED_STRING_CACHE_BYTES || sharedStringsCache.size > MAX_SHARED_STRING_CACHE_ENTRIES) {
    const oldest = sharedStringsCache.keys().next().value;
    sharedStringsCacheBytes -= sharedStringCacheSize(sharedStringsCache.get(oldest));
    sharedStringsCache.delete(oldest);
  }
}

// OOXML stores shared-string cells as ordinal references into one SST part. A cold DEFLATE lookup has no per-item seek point, so the stream must advance through the highest requested index. Requested entries are emitted as soon as encountered, and later requests use the bounded cache.
async function selectedSharedStrings(buffer, path, wanted, onProgress) {
  const result = Object.create(null);
  if (!wanted.size) return result;
  if (sharedStringsURL) {
    if (typeof fetch !== 'function') throw new Error('Preview string index is unavailable');
    const deadline = Date.now() + 14000;
    let indexed;
    while (!indexed) {
      const response = await fetch(sharedStringsURL, {
        method: 'POST',
        credentials: 'same-origin',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify([...wanted])
      });
      if (response.status === 202) {
        if (Date.now() >= deadline) throw new Error('Spreadsheet shared strings took too long to index');
        await new Promise(resolve => setTimeout(resolve, 100));
        continue;
      }
      if (!response.ok) throw new Error('Spreadsheet shared-string index is unavailable');
      indexed = await response.json();
    }
    const resolved = Object.create(null);
    for (const index of wanted) {
      const key = String(index);
      if (!Object.prototype.hasOwnProperty.call(indexed, key)) {
        throw new Error('A referenced spreadsheet string is missing from its preview index');
      }
      // The index has decoded XML entities already. Decode OOXML escapes once,
      // preserving escaped underscores and UTF-16 surrogate pairs.
      const entry = {t: String(indexed[key] ?? '').replace(/_x([\da-f]{4})_/gi, (_, hex) => String.fromCharCode(parseInt(hex, 16))).replace(/\r\n/g, '\n')};
      result[key] = entry;
      resolved[key] = entry;
      cacheSharedString(index, entry);
    }
    if (Object.keys(resolved).length) onProgress?.(resolved);
    return result;
  }
  const stream = zipEntryStream(buffer, path);
  if (!stream) return result;
  const reader = stream.getReader();
  const decoder = new TextDecoder('utf-8');
  let carry = '';
  let index = 0;
  let activeIndex = -1;
  let activeParts = null;
  let foundCount = 0;
  let finished = false;
  let progressEntries = Object.create(null);
  let progressCount = 0;
  let lastProgressAt = Date.now();
  let hasReportedProgress = false;
  const itemOpen = /<(?:[\w.-]+:)?si\b[^>]*>/i;
  const itemClose = /<\/(?:[\w.-]+:)?si\s*>/i;
  const flushProgress = force => {
    if (!onProgress || !progressCount || (!force && hasReportedProgress && progressCount < 64 && Date.now() - lastProgressAt < 100)) return;
    onProgress(progressEntries);
    progressEntries = Object.create(null);
    progressCount = 0;
    lastProgressAt = Date.now();
    hasReportedProgress = true;
  };
  const addResolved = (id, entry) => {
    if (!onProgress) result[id] = entry;
    progressEntries[id] = entry;
    progressCount++;
    foundCount++;
  };
  const consume = text => {
    carry += text;
    while (!finished) {
      if (activeIndex < 0) {
        const open = itemOpen.exec(carry);
        if (!open) { carry = carry.slice(-16); flushProgress(false); return; }
        const end = open.index + open[0].length;
        const id = index++;
        if (/\/>$/.test(open[0])) {
          if (wanted.has(id)) addResolved(id, XLSX.parse_sst_item('', {cellHTML:false}));
          carry = carry.slice(end);
          if (foundCount === wanted.size) finished = true;
          continue;
        }
        activeIndex = id;
        activeParts = wanted.has(id) ? [] : null;
        carry = carry.slice(end);
      } else {
        const close = itemClose.exec(carry);
        if (close) {
          if (activeParts !== null) {
            activeParts.push(carry.slice(0, close.index));
            addResolved(activeIndex, XLSX.parse_sst_item(activeParts.join('').trim(), {cellHTML:false}));
          }
          carry = carry.slice(close.index + close[0].length);
          activeIndex = -1;
          activeParts = null;
          if (foundCount === wanted.size) finished = true;
        } else {
          const safeLength = Math.max(0, carry.length - 16);
          if (activeParts !== null) activeParts.push(carry.slice(0, safeLength));
          carry = carry.slice(safeLength);
          flushProgress(false);
          return;
        }
      }
    }
    flushProgress(finished);
  };
  try {
    while (!finished) {
      const next = await reader.read();
      if (next.done) { consume(decoder.decode()); break; }
      consume(decoder.decode(next.value, {stream:true}));
    }
  } finally {
    if (!finished) await reader.cancel();
    reader.releaseLock();
  }
  return result;
}

function xmlAttributes(tag) {
  const attributes = Object.create(null);
  const pattern = /([^\s=<>/]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;
  let match;
  while ((match = pattern.exec(tag))) attributes[match[1]] = xmlUnescape(match[2] ?? match[3] ?? '');
  return attributes;
}

async function xlsxWorksheetPath(index) {
  // Content-type entries need not follow tab order. Resolve each tab's r:id
  // against the workbook relationships instead of indexing Directory.sheets.
  if (!worksheetPathsPromise) worksheetPathsPromise = readXlsxWorksheetPaths();
  return (await worksheetPathsPromise)[index] || null;
}

async function readXlsxWorksheetPaths() {
  const workbookPath = workbookMetadata.Directory.workbooks[0].replace(/^\/+/, '');
  const slash = workbookPath.lastIndexOf('/');
  const directory = workbookPath.slice(0, slash + 1);
  const relationshipsPath = directory + '_rels/' + workbookPath.slice(slash + 1) + '.rels';
  const stream = zipEntryStream(source, relationshipsPath);
  if (!stream) throw new Error('Workbook relationships are unavailable');
  const reader = stream.getReader(), decoder = new TextDecoder('utf-8');
  let xml = '', bytes = 0;
  try {
    while (true) {
      const next = await reader.read();
      if (next.done) break;
      bytes += next.value.byteLength;
      if (bytes > 4 * 1024 * 1024) throw new Error('Workbook relationships exceed preview limits');
      xml += decoder.decode(next.value, {stream:true});
    }
    xml += decoder.decode();
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
  const targets = new Map();
  for (const match of xml.matchAll(/<(?:[\w.-]+:)?Relationship\b[^>]*>/g)) {
    const attributes = xmlAttributes(match[0]);
    if (!attributes.Id || !attributes.Target || attributes.TargetMode === 'External') continue;
    const target = attributes.Target.replace(/\\/g, '/');
    const parts = [];
    for (const part of (target.startsWith('/') ? target : directory + target).split('/')) {
      if (part === '..') parts.pop();
      else if (part && part !== '.') parts.push(part);
    }
    targets.set(attributes.Id, parts.join('/'));
  }
  return workbookMetadata.Workbook.Sheets.map(sheet => targets.get(sheet.id));
}

function xlsxStyleFormat(styleIndex) {
  const styles = workbookMetadata && workbookMetadata.Styles;
  const style = styles && styles.CellXf && styles.CellXf[styleIndex || 0];
  if (!style) return undefined;
  const id = Number(style.numFmtId);
  return styles.NumberFmt && styles.NumberFmt[id] || workbookMetadata.SSF && workbookMetadata.SSF[id];
}

function xlsxCellFromXML(cell, content) {
  const valueMatch = /<(?:[\w.-]+:)?v\b[^>]*>([\s\S]*?)<\/(?:[\w.-]+:)?v\s*>/i.exec(content);
  const attributes = cell.attributes;
  const type = attributes.t || 'n';
  let value;
  let cellType = type;
  if (type === 's') {
    if (!valueMatch) return null;
    value = Number(xmlUnescape(valueMatch[1]).trim());
  } else if (type === 'inlineStr') {
    const inline = /<(?:[\w.-]+:)?is\b[^>]*>([\s\S]*?)<\/(?:[\w.-]+:)?is\s*>/i.exec(content);
    const parsed = inline ? XLSX.parse_sst_item(inline[1], {cellHTML:false}) : {t:''};
    cellType = 's';
    value = parsed.t;
    cell.r = parsed.r;
  } else {
    if (!valueMatch) return null;
    const raw = xmlUnescape(valueMatch[1]).trim();
    if (type === 'b') value = raw === '1' || raw === 'true';
    else if (type === 'e') { cellType = 's'; value = raw; }
    else if (type === 'd') {
      cellType = 'n';
      value = XLSX.parse_xlsx_date(raw, !!(workbookMetadata && workbookMetadata.Workbook && workbookMetadata.Workbook.WBProps && workbookMetadata.Workbook.WBProps.date1904));
    }
    else if (type === 'str') {
      cellType = 's';
      // SheetJS decodes <v> first, then decodes formula-result strings again.
      // Preserve that behavior, including escaped underscores and XML entities.
      value = XLSX.parse_xlsx_string(XLSX.parse_xlsx_string(valueMatch[1]));
    }
    else value = raw === '' ? '' : Number(raw);
  }
  const result = {t:cellType, v:value};
  const format = xlsxStyleFormat(Number(attributes.s || 0));
  if (format) result.z = format;
  if (type === 's') result._sst = true;
  else result.w = XLSX.utils.format_cell(result, undefined, {date1904:!!(workbookMetadata && workbookMetadata.Workbook && workbookMetadata.Workbook.WBProps && workbookMetadata.Workbook.WBProps.date1904)});
  if (cell.r) result.r = cell.r;
  return result;
}

async function streamXlsxWorksheet(index, capture, cellCapacity) {
  const path = await xlsxWorksheetPath(index);
  const stream = path && zipEntryStream(source, path);
  if (!stream) throw new Error('Worksheet is unavailable');
  const reader = stream.getReader();
  const decoder = new TextDecoder('utf-8');
  let carry = '';
  let cell = null;
  let rowIndex = -1;
  let nextRowIndex = 0;
  let nextColumnIndex = 0;
  let totalRows = 0;
  let totalColumns = 0;
  let dimensionKnown = false;
  let stopped = false;
  let skipRowTail = false;
  let previewRowLimit = MAX_ROWS;
  let previewColumns = 1;
  let capturedCellCount = 0;
  const cells = Object.create(null);
  const capacity = Math.max(0, Math.min(MAX_SHEET_CELLS, cellCapacity));

  const resizePreview = () => {
    const columns = Math.min(totalColumns || 1, MAX_COLUMNS);
    const rows = columns ? Math.min(MAX_ROWS, Math.floor(capacity / columns)) : 0;
    if (columns === previewColumns && rows === previewRowLimit) return;
    previewColumns = columns;
    previewRowLimit = rows;
    for (const address of Object.keys(cells)) {
      const coordinate = XLSX.utils.decode_cell(address);
      if (coordinate.r >= previewRowLimit || coordinate.c >= columns) {
        delete cells[address];
        capturedCellCount--;
      }
    }
  };
  const addCoordinate = (row, column) => {
    totalRows = Math.max(totalRows, row + 1);
    totalColumns = Math.max(totalColumns, column + 1);
    resizePreview();
  };
  const parseTag = tag => {
    if (tag.startsWith('<!--') || tag.startsWith('<?') || tag.startsWith('<!')) return;
    const nameMatch = /^<\/?(?:[\w.-]+:)?([\w.-]+)/.exec(tag);
    if (!nameMatch) return;
    const name = nameMatch[1].toLowerCase();
    const closing = /^<\//.test(tag);
    const selfClosing = /\/\s*>$/.test(tag);
    if (!closing && name === 'dimension') {
      const ref = xmlAttributes(tag).ref;
      if (ref) {
        const range = XLSX.utils.decode_range(ref);
        totalRows = Math.max(totalRows, range.e.r + 1);
        totalColumns = Math.max(totalColumns, range.e.c + 1);
        dimensionKnown = true;
        resizePreview();
        // Dimension-only probes must not inflate the skipped sheet's cells.
        if (!capture) stopped = true;
      }
      return;
    }
    if (!closing && name === 'row') {
      const attributes = xmlAttributes(tag);
      rowIndex = attributes.r ? Math.max(0, Number(attributes.r) - 1) : nextRowIndex;
      nextRowIndex = rowIndex + 1;
      nextColumnIndex = 0;
      addCoordinate(rowIndex, 0);
      if (capture && dimensionKnown && totalRows > previewRowLimit && rowIndex >= previewRowLimit) stopped = true;
      return;
    }
    if (!closing && name === 'c') {
      const attributes = xmlAttributes(tag);
      const coordinate = attributes.r ? XLSX.utils.decode_cell(attributes.r) : {r:Math.max(rowIndex,0),c:nextColumnIndex};
      rowIndex = coordinate.r;
      nextColumnIndex = coordinate.c + 1;
      if (capture && dimensionKnown && coordinate.c >= MAX_COLUMNS) {
        // Only cancel after the last preview row; earlier wide row tails
        // must be skipped so later visible rows can still be read.
        if (coordinate.r + 1 >= Math.min(totalRows, previewRowLimit)) stopped = true;
        else skipRowTail = true;
        return;
      }
      addCoordinate(coordinate.r, coordinate.c);
      const address = XLSX.utils.encode_cell(coordinate);
      const keep = capture && coordinate.r < previewRowLimit && coordinate.c < MAX_COLUMNS && capacity > 0;
      cell = {address, attributes, parts:[], keep};
      if (selfClosing) {
        if (keep && capturedCellCount < capacity) {
          const parsed = xlsxCellFromXML(cell, '');
          if (parsed) { cells[address] = parsed; capturedCellCount++; }
        }
        cell = null;
      }
      return;
    }
    if (cell) {
      if (closing && name === 'c') {
        if (cell.keep && capturedCellCount < capacity) {
          const parsed = xlsxCellFromXML(cell, cell.parts.join(''));
          if (parsed) { cells[cell.address] = parsed; capturedCellCount++; }
        }
        cell = null;
      } else if (cell.keep) cell.parts.push(tag);
    }
  };
  const process = (text, final = false) => {
    carry += text;
    let offset = 0;
    while (!stopped && offset < carry.length) {
      if (skipRowTail) {
        const closingRow = /<\/(?:[\w.-]+:)?row\s*>/i.exec(carry.slice(offset));
        if (!closingRow) {
          carry = carry.slice(Math.max(offset, carry.length - 32));
          return;
        }
        offset += closingRow.index + closingRow[0].length;
        skipRowTail = false;
        continue;
      }
      const open = carry.indexOf('<', offset);
      if (open < 0) {
        if (cell && cell.keep) cell.parts.push(carry.slice(offset));
        carry = '';
        return;
      }
      if (cell && cell.keep && open > offset) cell.parts.push(carry.slice(offset, open));
      const end = xmlTagEnd(carry, open);
      if (end < 0) {
        // Text before this tag has already been captured. Retaining it would
        // duplicate values when a closing tag spans decoded chunks.
        carry = carry.slice(open);
        return;
      }
      parseTag(carry.slice(open, end + 1));
      offset = end + 1;
    }
    carry = stopped ? '' : carry.slice(offset);
    if (final && carry && cell && cell.keep) cell.parts.push(carry);
    if (final) carry = '';
  };

  try {
    while (!stopped) {
      const next = await reader.read();
      if (next.done) { process(decoder.decode(), true); break; }
      process(decoder.decode(next.value, {stream:true}));
    }
  } finally {
    if (stopped) await reader.cancel();
    reader.releaseLock();
  }
  totalRows = Math.min(totalRows, 1048576);
  totalColumns = Math.min(totalColumns, 16384);
  const fullref = totalRows && totalColumns
    ? 'A1:' + XLSX.utils.encode_cell({r:totalRows - 1,c:totalColumns - 1})
    : undefined;
  const sstPath = workbookMetadata && workbookMetadata.Directory && workbookMetadata.Directory.strs && workbookMetadata.Directory.strs[0];
  const sheet = {'!ref':fullref, '!fullref':fullref, '!sstPath':sstPath && sstPath.replace(/^\/+/, ''), ...cells};
  return {sheet, totalRows, totalColumns};
}

function remainingCellCapacity(index) {
  let remaining = MAX_WORKBOOK_CELLS;
  for (let current = 0; current < index; current++) {
    const {totalRows, totalColumns} = dimensions.get(current) || {totalRows:0,totalColumns:0};
    const columns = Math.min(totalColumns, MAX_COLUMNS);
    const rows = columns ? Math.min(totalRows, MAX_ROWS, Math.floor(Math.min(MAX_SHEET_CELLS, remaining) / columns)) : 0;
    remaining -= rows * columns;
  }
  return Math.max(0, Math.min(MAX_SHEET_CELLS, remaining));
}

function sheetHasPendingStrings(sheet) {
  return sheetCells(sheet).some(cell => cell._sst);
}

function previewWithPendingStrings(index, workbook) {
  const name = sheetNames[index];
  const original = workbook && workbook.Sheets && workbook.Sheets[name];
  if (!original) return makeSheetPreview(index, workbook);
  const placeholder = {...original};
  for (const key of Object.keys(placeholder)) {
    const cell = placeholder[key];
    if (cell && cell._sst) placeholder[key] = {...cell, v:'', w:''};
  }
  const preview = makeSheetPreview(index, {SheetNames:sheetNames,Sheets:{[name]:placeholder}});
  if (sheetHasPendingStrings(original)) preview.valuesPending = true;
  return preview;
}

async function loadXlsxZipSheet(index, requestId) {
  if (!Number.isInteger(index) || index < 0 || index >= sheetNames.length) throw new Error('Worksheet is unavailable');
  for (let current = 0; current < index; current++) {
    if (!dimensions.has(current)) {
      const probe = await streamXlsxWorksheet(current, false, 0);
      dimensions.set(current, {totalRows:probe.totalRows,totalColumns:probe.totalColumns});
    }
  }
  const parsed = await streamXlsxWorksheet(index, true, remainingCellCapacity(index));
  dimensions.set(index, {totalRows:parsed.totalRows,totalColumns:parsed.totalColumns});
  const workbook = {SheetNames:sheetNames, Sheets:{[sheetNames[index]]:parsed.sheet}};
  const type = requestId == null ? 'ready' : 'sheet';
  if (sheetHasPendingStrings(parsed.sheet)) {
    postMessage({type,names:sheetNames,index,sheet:previewWithPendingStrings(index,workbook),requestId,updatesPending:true});
    try {
      await resolveSheetStrings(parsed.sheet, source, () => {
        postMessage({type:'update',index,sheet:previewWithPendingStrings(index,workbook),updatesPending:true});
      });
      postMessage({type:'update',index,sheet:makeSheetPreview(index,workbook),updatesPending:false});
    } catch (error) {
      postMessage({type:'update-error',index,error:'Some spreadsheet text could not be resolved',updatesPending:false});
    }
    return;
  }
  postMessage({type,names:sheetNames,index,sheet:makeSheetPreview(index,workbook),requestId});
}

function sheetCells(sheet) {
  const cells = [];
  const dense = sheet && sheet['!data'];
  if (dense) {
    for (const row of dense) if (row) for (const cell of row) if (cell && cell._sst) cells.push(cell);
  } else if (sheet) {
    for (const key of Object.keys(sheet)) if (sheet[key] && sheet[key]._sst) cells.push(sheet[key]);
  }
  return cells;
}

function resolveSheetStrings(sheet, buffer, onProgress) {
  if (!sheet) return null;
  const cells = sheetCells(sheet);
  const cellsByIndex = new Map();
  const wanted = new Set();
  for (const cell of cells) {
    const index = Number(cell.v);
    const entry = getCachedSharedString(index);
    if (entry) {
      cell.v = entry.t;
      cell.w = cell.v;
      if (entry.r) cell.r = entry.r;
      else delete cell.r;
      delete cell._sst;
    } else {
      wanted.add(index);
      if (!cellsByIndex.has(index)) cellsByIndex.set(index, []);
      cellsByIndex.get(index).push(cell);
    }
  }
  if (!wanted.size || !sheet['!sstPath']) {
    delete sheet['!sstPath'];
    return null;
  }
  const applyStrings = strings => {
    let changed = false;
    for (const key of Object.keys(strings)) {
      const index = Number(key), entry = strings[key];
      cacheSharedString(index, entry);
      for (const cell of cellsByIndex.get(index) || []) {
        if (!cell._sst) continue;
        cell.v = entry.t;
        cell.w = cell.v;
        if (entry.r) cell.r = entry.r;
        else delete cell.r;
        delete cell._sst;
        changed = true;
      }
    }
    if (changed) onProgress?.();
  };
  return selectedSharedStrings(buffer, sheet['!sstPath'], wanted, applyStrings).then(strings => {
    applyStrings(strings);
    for (const cell of cells) {
      if (!cell._sst) continue;
      cell.v = '';
      cell.w = '';
      delete cell._sst;
    }
    delete sheet['!sstPath'];
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

function scanDelimited(buffer, requestedFormat) {
  let delimiter = requestedFormat === 'tsv' ? '\t' : null;
  const bytes = new Uint8Array(buffer);
  // Excel's Unicode text exports use UTF-16. Inspect only the BOM and keep
  // decoding in bounded chunks; TextDecoder removes the matching BOM.
  const encoding = bytes[0] === 0xff && bytes[1] === 0xfe ? 'utf-16le'
    : bytes[0] === 0xfe && bytes[1] === 0xff ? 'utf-16be' : 'utf-8';
  const decoder = new TextDecoder(encoding);
  const previewRows = [];
  let totalRows = 0;
  let totalColumns = 0;
  let row = [];
  let field = '';
  let rowIndex = 0;
  let columnIndex = 0;
  let quoted = false;
  let pendingQuote = false;
  let skipLineFeed = false;
  let recordStarted = false;
  let fieldStarted = false;

  const rowLimit = () => Math.min(MAX_ROWS, Math.floor(Math.min(MAX_SHEET_CELLS, MAX_WORKBOOK_CELLS) / Math.min(MAX_COLUMNS, Math.max(totalColumns, 1))));
  const append = value => {
    if (rowIndex < rowLimit() && columnIndex < MAX_COLUMNS) field += value;
    if (value) { recordStarted = true; fieldStarted = true; }
  };
  const updateDimensions = () => {
    totalColumns = Math.max(totalColumns, columnIndex + 1);
    const limit = rowLimit();
    if (previewRows.length > limit) previewRows.length = limit;
    if (rowIndex >= limit) row = [];
  };
  const finishField = () => {
    updateDimensions();
    if (rowIndex < rowLimit() && columnIndex < MAX_COLUMNS) row[columnIndex] = field;
    field = '';
    fieldStarted = false;
    columnIndex++;
    recordStarted = true;
  };
  const finishRow = () => {
    finishField();
    totalRows = Math.max(totalRows, rowIndex + 1);
    if (rowIndex < rowLimit()) previewRows[rowIndex] = row;
    row = [];
    rowIndex++;
    columnIndex = 0;
    recordStarted = false;
  };
  const consume = (text, final = false) => {
    let i = 0;
    if (pendingQuote) {
      if (!text.length && !final) return;
      if (text[0] === '"') {
        append('"');
        i = 1;
      } else {
        quoted = false;
      }
      pendingQuote = false;
    }
    for (; i < text.length; i++) {
      const char = text[i];
      if (skipLineFeed) {
        skipLineFeed = false;
        if (char === '\n') continue;
      }
      if (quoted) {
        if (char === '"') {
          if (text[i + 1] === '"') {
            append('"');
            i++;
          } else if (i === text.length - 1 && !final) {
            pendingQuote = true;
          } else {
            quoted = false;
          }
        } else {
          append(char);
        }
      } else if (char === '"' && !fieldStarted) {
        quoted = true;
        fieldStarted = true;
        recordStarted = true;
      } else if (char === delimiter) {
        finishField();
      } else if (char === '\n' || char === '\r') {
        finishRow();
        if (char === '\r') skipLineFeed = true;
      } else {
        append(char);
      }
    }
    if (final && pendingQuote) {
      pendingQuote = false;
      quoted = false;
    }
  };

  const delimiterCounts = {'\t': 0, ',': 0, ';': 0, '|': 0};
  const pendingChunks = [];
  let sampleBytes = 0;
  let unknownMode = false;
  let detectingQuoted = false;
  let detectingPendingQuote = false;
  let detectingFieldStarted = false;
  let unknownQuoted = false;
  let unknownPendingQuote = false;
  const chooseDelimiter = () => Object.keys(delimiterCounts).reduce(
    (best, candidate) => delimiterCounts[candidate] > delimiterCounts[best] ? candidate : best,
    ','
  );
  const detectDelimiter = text => {
    let i = 0;
    if (detectingPendingQuote) {
      if (text[0] === '"') i = 1;
      else detectingQuoted = false;
      detectingPendingQuote = false;
    }
    for (; i < text.length; i++) {
      const char = text[i];
      if (detectingQuoted) {
        if (char === '"') {
          if (text[i + 1] === '"') i++;
          else if (i === text.length - 1) detectingPendingQuote = true;
          else detectingQuoted = false;
        }
      } else if (char === '"' && !detectingFieldStarted) {
        detectingQuoted = true;
        detectingFieldStarted = true;
      } else if (char === '\n' || char === '\r') {
        detectingFieldStarted = false;
        // Title-only and blank rows provide no separator evidence.
        if (Object.values(delimiterCounts).some(count => count > 0)) {
          delimiter = chooseDelimiter();
          return;
        }
      } else if (Object.prototype.hasOwnProperty.call(delimiterCounts, char)) {
        delimiterCounts[char]++;
        detectingFieldStarted = false;
      } else {
        detectingFieldStarted = true;
      }
    }
  };
  const consumeUndetermined = (text, final = false) => {
    let i = 0;
    if (unknownPendingQuote) {
      if (!text.length && !final) return;
      if (text[0] === '"') {
        append('"');
        i = 1;
      } else {
        unknownQuoted = false;
      }
      unknownPendingQuote = false;
    }
    for (; i < text.length; i++) {
      const char = text[i];
      if (skipLineFeed) {
        skipLineFeed = false;
        if (char === '\n') continue;
      }
      if (unknownQuoted) {
        if (char === '"') {
          if (text[i + 1] === '"') {
            append('"');
            i++;
          } else if (i === text.length - 1 && !final) {
            unknownPendingQuote = true;
          } else {
            unknownQuoted = false;
          }
        } else {
          append(char);
        }
      } else if (char === '"' && !fieldStarted) {
        unknownQuoted = true;
        fieldStarted = true;
        recordStarted = true;
      } else if (Object.prototype.hasOwnProperty.call(delimiterCounts, char)) {
        delimiter = char;
        unknownMode = false;
        finishField();
        consume(text.slice(i + 1), final);
        return;
      } else if (char === '\n' || char === '\r') {
        finishRow();
        if (char === '\r') skipLineFeed = true;
      } else {
        append(char);
      }
    }
    if (final && unknownPendingQuote) {
      unknownPendingQuote = false;
      unknownQuoted = false;
    }
  };
  const replayPendingChunks = consumeChunk => {
    for (const pending of pendingChunks) consumeChunk(pending);
    pendingChunks.length = 0;
  };
  let firstChunk = true;
  const processChunk = (chunk, byteLength) => {
    if (firstChunk) {
      firstChunk = false;
      // A leading Excel separator directive is metadata, not a worksheet row.
      const directive = /^sep=(.)(?:\r\n|\r|\n)/.exec(chunk);
      if (directive) {
        delimiter = directive[1];
        chunk = chunk.slice(directive[0].length);
      }
    }
    if (delimiter !== null) {
      consume(chunk);
      return;
    }
    if (unknownMode) {
      consumeUndetermined(chunk);
      return;
    }
    sampleBytes += byteLength;
    if (chunk) pendingChunks.push(chunk);
    detectDelimiter(chunk);
    if (delimiter !== null) {
      replayPendingChunks(consume);
    } else if (sampleBytes >= MAX_DELIMITER_SAMPLE_BYTES) {
      if (Object.values(delimiterCounts).some(count => count > 0)) {
        delimiter = chooseDelimiter();
        replayPendingChunks(consume);
      } else {
        unknownMode = true;
        replayPendingChunks(consumeUndetermined);
      }
    }
  };

  for (let offset = 0; offset < bytes.length; offset += MAX_DELIMITER_SAMPLE_BYTES) {
    const byteChunk = bytes.subarray(offset, Math.min(offset + MAX_DELIMITER_SAMPLE_BYTES, bytes.length));
    const chunk = decoder.decode(byteChunk, {stream: true});
    processChunk(chunk, byteChunk.byteLength);
  }
  const tail = decoder.decode();
  if (tail) processChunk(tail, 0);
  if (delimiter === null) {
    delimiter = chooseDelimiter();
    if (unknownMode) unknownMode = false;
    else replayPendingChunks(consume);
  }
  consume('', true);
  if (recordStarted || columnIndex > 0 || field.length > 0 || totalRows === 0) finishRow();

  const columns = Math.min(totalColumns, MAX_COLUMNS);
  const rowCount = columns
    ? Math.min(totalRows, MAX_ROWS, Math.floor(Math.min(MAX_SHEET_CELLS, MAX_WORKBOOK_CELLS) / columns))
    : 0;
  const rows = Array.from({length: rowCount}, (_, rowNumber) =>
    Array.from({length: columns}, (_, column) => String(previewRows[rowNumber]?.[column] || ''))
  );
  return {totalRows, totalColumns, rows};
}

function readDelimited(buffer, requestedFormat) {
  const preview = scanDelimited(buffer, requestedFormat);
  return {
    name: 'Sheet1',
    rows: preview.rows,
    totalRows: preview.totalRows,
    totalColumns: preview.totalColumns,
    truncated: preview.totalRows > preview.rows.length || preview.totalColumns > Math.min(preview.totalColumns, MAX_COLUMNS)
  };
}

function parseODSTable(rootOpen, preamble, tableXML, rowLimit) {
  return XLSX.read(makeODSDocument(rootOpen, preamble, tableXML), {
    type: 'string',
    sheetRows: rowLimit,
    sheetCols: MAX_COLUMNS,
    cellHTML: false
  });
}

async function loadODSZipSheet(index, requestId) {
  if (!Number.isInteger(index) || index < 0) throw new Error('Worksheet is unavailable');
  let emitted = false;
  const stopAtTarget = requestId != null;
  const publish = (type, tableIndex, name, tableXML, rootOpen, preamble, metrics) => {
    sheetNames[tableIndex] = name;
    dimensions.set(tableIndex, metrics);
    if (tableIndex !== index) return;
    const allocation = sheetAllocation(index);
    const workbook = tableXML && allocation.rowCount
      ? parseODSTable(rootOpen, preamble, tableXML, allocation.rowCount)
      : {SheetNames:[name],Sheets:{[name]:{}}};
    const message = {
      type,
      names: sheetNames.slice(),
      catalogPending: type === 'ready' && requestId == null,
      // Later tabs have no catalog timer: keep their update timer active until
      // the remaining dimension scan finishes, then explicitly clear it.
      updatesPending: !!metrics.dimensionsPending,
      index,
      sheet: {...makeSheetPreview(index, workbook), dimensionsPending:!!metrics.dimensionsPending}
    };
    if (metrics.dimensionsPending) message.sheet.truncated = null;
    if (type === 'ready' || type === 'sheet') message.requestId = requestId;
    postMessage(message);
    emitted = true;
  };
  await streamODSTables(source, index, (tableIndex, name, tableXML, rootOpen, preamble, metrics) => {
    publish(emitted ? 'update' : requestId == null ? 'ready' : 'sheet', tableIndex, name, tableXML, rootOpen, preamble, metrics);
  }, names => {
    sheetNames = names;
    postMessage({type: 'catalog', names: names.slice()});
  }, stopAtTarget, (tableIndex, name, tableXML, rootOpen, preamble, metrics) => {
    if (!emitted) publish(requestId == null ? 'ready' : 'sheet', tableIndex, name, tableXML, rootOpen, preamble, metrics);
  });
  if (!emitted) throw new Error('Worksheet is unavailable');
}

function loadSheet(index, requestId, initialProbe) {
  const fail = error => {
    postMessage({type: 'error', error: 'Could not read this spreadsheet: ' + (error.message || error), requestId});
    if (requestId == null) close();
  };
  try {
    if (['xlsx','xlsm','xltx','xltm'].includes(format) && isZipBuffer(source) && workbookMetadata) {
      loadXlsxZipSheet(index, requestId).catch(fail);
      return;
    }
    if (format === 'ods' && isZipBuffer(source)) {
      loadODSZipSheet(index, requestId).catch(fail);
      return;
    }
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
    const finish = () => {
      const preview = makeSheetPreview(index, workbook);
      postMessage({type: requestId == null ? 'ready' : 'sheet', names: sheetNames, index, sheet: preview, requestId});
    };
    const resolving = resolveSheetStrings(workbook && workbook.Sheets && workbook.Sheets[sheetNames[index]], source);
    if (resolving) resolving.then(finish).catch(fail);
    else finish();
  } catch (error) {
    fail(error);
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
    sharedStringsURL = message && message.sharedStringsURL;
    if (format === 'csv' || format === 'tsv') {
      const sheet = readDelimited(source, format);
      source = null;
      sheetNames = [sheet.name];
      postMessage({type: 'ready', names: sheetNames, index: 0, sheet});
      return;
    }
    if (format === 'ods' && isZipBuffer(source)) {
      sheetNames = [];
      loadSheet(0, null);
      return;
    }
    if (['xlsx','xlsm','xltx','xltm'].includes(format) && isZipBuffer(source)) {
      workbookMetadata = readWorkbook({sheets:[]});
      worksheetPathsPromise = undefined;
      sheetNames = workbookMetadata.SheetNames || [];
      if (!sheetNames.length) {
        postMessage({type:'ready',names:[],index:-1,sheet:null});
        return;
      }
      loadSheet(0, null);
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
