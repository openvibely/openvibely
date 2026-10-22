# Formula and workflow checks

Use the sections relevant to the requested workbook. These are checks, not a reason to add tabs or a separate audit report.

## Formula design

Prefer familiar, inspectable formulas and one owner for each calculation. Preserve valid existing formulas unless the task requires changing them. Use bounded, aligned ranges. Choose SUM for addition, PRODUCT for multiplication, and SUMPRODUCT for aligned row-wise products; missing factors must not silently become valid zero results.

For new conditional counts, sums and averages, prefer COUNTIFS, SUMIFS and AVERAGEIFS even with one condition. Do not introduce LET, array/spill formulas, MAP, REDUCE or LAMBDA; use familiar formulas and labeled intermediate steps. Ordinary range arguments in SUMIFS and SUMPRODUCT are fine. Preserve required existing/template formulas during narrow edits.

Choose exact versus approximate lookup intentionally. Confirm whether duplicate keys require aggregation instead of first-match lookup. Distinguish a missing lookup value from a legitimate zero; a zero SUMIFS result does not establish that matching records exist. Keep missing-data checks explicit rather than wrapping every formula in IFERROR.

Use invariant Excel number-format syntax; do not swap comma/decimal characters to imitate a locale. Match displayed precision to counts, rates and decision outputs. Keep counts and identifiers exact; do not invent scores or confidence precision.

## Periods and scenarios

Use the source fiscal calendar, reporting date and year; do not invent a missing year. Share chronological period headers across related schedules. For monthly timestamp aggregation, use inclusive month start and exclusive next-month start. Sum flows, take ending stocks, and calculate rates from matching period components.

For week-based work with no supplied convention, make a Monday-start assumption explicit; use only supplied holiday calendars. For a nonnegative annual flow forecast, do not create a negative remaining-period forecast to force a full-year target below actuals; expose the inconsistency.

## Operational workbooks

Keep each incomplete human prerequisite visible. Exercise status transitions, filters and rankings with representative changes. Key notes and overrides by stable IDs so sorting or refresh does not move them to another record. Distinguish a true zero, missing input, pending review and stale comparison result; do not manufacture completion.

Bound conditional formatting to useful ranges, make valid states neutral and missing/failed states distinct, and verify colors change when data changes. Do not simulate validation by painting cells static error colors.

Use a cover only if useful, placed first; an explanatory readme belongs last. Avoid unnecessary relay tabs. Labels should identify the population, period, units and material uncertainty, not narrate the tool workflow.

## Sources and review annotations

Keep source references beside their inputs. When a cell annotation is appropriate, use the supported `workbook-edit` `note` operation; ordinary source notes do not need threaded conversations. Avoid repeating the same citation in cells, notes and threads. Preserve existing annotations; update an existing note/comment/thread rather than adding another annotation type to the same cell. Do not add citations, notes or comments to cover/presentation tabs or output regions unless requested. On mixed-use sheets, place sources beside inputs, outside the output region. Create comments or review threads only when requested; use the user’s supplied name or `User` as author.

## Charts

Use descending ordering for rankings unless source order carries meaning. For distributions, derive bins/counts with formulas and plot those values if the desired native chart is unavailable; disclose a material substitution. Include the relevant population rather than cherry-picking categories.

Make scale, units and currency explicit. A label saying “$M” must agree with actual axis scaling, not merely rename raw dollar values. Use one series color unless color encodes a meaningful distinction, and choose readable legends or direct labels. Reserve blank space for chart objects instead of covering data.

If dates render as incorrect categories, inspect their types and bindings, create a formula-backed normalized helper range if necessary, and retry once. If the renderer still disagrees, report the limitation rather than repeatedly rebuilding the workbook.

On other command failures, read the error and relevant command help, correct the smallest affected operation and retry with a new output. Preserve the existing workbook; repeated wholesale rebuilds can lose unsupported content.
