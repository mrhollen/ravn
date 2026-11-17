// Pure dashboard chart math and inline SVG geometry helpers for
// GET /projects/{id}/report/history (throughput bars + cumulative-completed
// burn-up curve). Pure data in -> view models / SVG strings out; no DOM or
// Alpine dependencies so the entire module stays fast and unit-testable.

import type {
  ProjectReportHistory,
  ReportHistoryBucket,
  ReportHistoryPoint,
} from "../types";
import {
  CHART_AXIS_TEXT_FILL,
  CHART_GRID_STROKE,
  CUMULATIVE_AREA_FILL,
  CUMULATIVE_DOT_CLASSES,
  CUMULATIVE_LINE_STROKE,
  THROUGHPUT_BAR_FILL,
} from "../ui/tokens";

export const DEFAULT_HISTORY_DAYS = 30;
export const DEFAULT_HISTORY_BUCKET: ReportHistoryBucket = "day";

export interface ChartGeometry {
  readonly width: number;
  readonly height: number;
  readonly padTop: number;
  readonly padRight: number;
  readonly padBottom: number;
  readonly padLeft: number;
}

export const CHART_GEOMETRY: ChartGeometry = {
  width: 640,
  height: 220,
  padTop: 18,
  padRight: 24,
  padBottom: 34,
  padLeft: 40,
};

export interface YTick {
  value: number;
  y: number;
  label: string;
}

export interface XTick {
  index: number;
  x: number;
  label: string;
}

export interface ThroughputBar {
  index: number;
  bucketStart: string;
  label: string;
  completed: number;
  opened: number;
  x: number;
  y: number;
  width: number;
  height: number;
  tooltip: string;
}

export interface ThroughputSummary {
  totalCompleted: number;
  totalOpened: number;
  averagePerBucket: number;
  peakCompleted: number;
  peakLabel: string;
  bucketCount: number;
  bucket: ReportHistoryBucket;
}

export interface ThroughputChartModel {
  empty: boolean;
  bucket: ReportHistoryBucket;
  days: number;
  yMax: number;
  yTicks: YTick[];
  xTicks: XTick[];
  bars: ThroughputBar[];
  summary: ThroughputSummary;
}

export interface CumulativePoint {
  index: number;
  bucketStart: string;
  label: string;
  cumulativeCompleted: number;
  completed: number;
  x: number;
  y: number;
  tooltip: string;
}

export interface CumulativeSummary {
  latestTotal: number;
  startTotal: number;
  windowGain: number;
  bucketCount: number;
  bucket: ReportHistoryBucket;
}

export interface CumulativeChartModel {
  empty: boolean;
  bucket: ReportHistoryBucket;
  days: number;
  yMax: number;
  yTicks: YTick[];
  xTicks: XTick[];
  points: CumulativePoint[];
  linePath: string;
  areaPath: string;
  summary: CumulativeSummary;
}

const UTC_MONTHS = [
  "Jan", "Feb", "Mar", "Apr", "May", "Jun",
  "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
] as const;

function clampNonNegInt(n: unknown): number {
  if (typeof n !== "number" || !Number.isFinite(n)) return 0;
  return Math.max(0, Math.trunc(n));
}

function round1(n: number): number {
  return Math.round(n * 10) / 10;
}

function fmtCoord(n: number): string {
  return Number.isInteger(n) ? String(n) : n.toFixed(2);
}

/** Escape XML text characters for safe interpolation into SVG <text>/<title>. */
export function escapeSvgText(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

/** Format an RFC3339 bucket timestamp in UTC ("Nov 16") without locale drift. */
export function formatBucketLabel(isoTimestamp: string): string {
  const ms = Date.parse(isoTimestamp);
  if (Number.isNaN(ms)) return isoTimestamp || "—";
  const d = new Date(ms);
  return `${UTC_MONTHS[d.getUTCMonth()]} ${d.getUTCDate()}`;
}

function formatTooltipPrefix(label: string, bucket: ReportHistoryBucket): string {
  return bucket === "week" ? `Week of ${label}` : label;
}

/** True when history is missing or has zero time buckets (empty project). */
export function isHistoryEmpty(history: ProjectReportHistory | null | undefined): boolean {
  return !history || !Array.isArray(history.series) || history.series.length === 0;
}

/** Normalize raw API payload (including malformed/partial mocks) into a safe series. */
export function normalizeHistory(
  history: ProjectReportHistory | null | undefined,
  fallbackBucket: ReportHistoryBucket = DEFAULT_HISTORY_BUCKET,
): { days: number; bucket: ReportHistoryBucket; series: ReportHistoryPoint[] } {
  const bucket: ReportHistoryBucket =
    history?.bucket === "week" || history?.bucket === "day" ? history.bucket : fallbackBucket;
  const days =
    typeof history?.days === "number" && Number.isFinite(history.days) && history.days > 0
      ? Math.trunc(history.days)
      : DEFAULT_HISTORY_DAYS;
  const rawSeries = Array.isArray(history?.series) ? history.series : [];
  const series: ReportHistoryPoint[] = rawSeries.map((pt) => ({
    bucket_start: typeof pt?.bucket_start === "string" ? pt.bucket_start : "",
    opened: clampNonNegInt(pt?.opened),
    completed: clampNonNegInt(pt?.completed),
    cumulative_completed: clampNonNegInt(pt?.cumulative_completed),
  }));
  return { days, bucket, series };
}

/** Compute clean integer Y-axis ticks from 0 to yMax >= maxValue. */
export function niceYScale(maxValue: number, targetSteps = 4): { yMax: number; ticks: number[] } {
  const safeMax = Math.max(0, Math.ceil(Number.isFinite(maxValue) ? maxValue : 0));
  if (safeMax <= 0) {
    return { yMax: 1, ticks: [0, 1] };
  }
  if (safeMax <= 4) {
    const ticks: number[] = [];
    for (let v = 0; v <= safeMax; v++) ticks.push(v);
    return { yMax: safeMax, ticks };
  }
  const rawStep = safeMax / Math.max(1, targetSteps);
  const mag = Math.pow(10, Math.floor(Math.log10(rawStep)));
  const norm = rawStep / mag;
  let stepFactor = 10;
  if (norm <= 1) stepFactor = 1;
  else if (norm <= 2) stepFactor = 2;
  else if (norm <= 5) stepFactor = 5;
  const step = Math.max(1, Math.round(stepFactor * mag));
  const yMax = Math.ceil(safeMax / step) * step;
  const ticks: number[] = [];
  for (let v = 0; v <= yMax; v += step) {
    ticks.push(v);
  }
  return { yMax, ticks };
}

/** Pick evenly spaced X-axis label indices, always preserving first and last. */
export function selectXTicks(
  labels: readonly string[],
  maxTicks = 6,
): Array<{ index: number; label: string }> {
  const n = labels.length;
  if (n === 0) return [];
  const cap = Math.max(2, maxTicks);
  if (n <= cap) {
    return labels.map((label, index) => ({ index, label }));
  }
  const chosen = new Set<number>([0, n - 1]);
  const intervals = cap - 1;
  for (let k = 1; k < intervals; k++) {
    chosen.add(Math.round((k * (n - 1)) / intervals));
  }
  return [...chosen]
    .sort((a, b) => a - b)
    .map((index) => ({ index, label: labels[index] }));
}

function buildYAxisTicks(maxValue: number, geom: ChartGeometry): { yMax: number; yTicks: YTick[] } {
  const plotHeight = geom.height - geom.padTop - geom.padBottom;
  const { yMax, ticks } = niceYScale(maxValue);
  const yTicks = ticks.map((value) => ({
    value,
    y: geom.padTop + plotHeight - (value / yMax) * plotHeight,
    label: String(value),
  }));
  return { yMax, yTicks };
}

function slotCenterX(index: number, count: number, geom: ChartGeometry): number {
  const plotWidth = geom.width - geom.padLeft - geom.padRight;
  const slotWidth = plotWidth / Math.max(1, count);
  return geom.padLeft + (index + 0.5) * slotWidth;
}

/** Build throughput (completed per day/week) chart model. */
export function buildThroughputChartModel(
  history: ProjectReportHistory | null | undefined,
  fallbackBucket: ReportHistoryBucket = DEFAULT_HISTORY_BUCKET,
  geom: ChartGeometry = CHART_GEOMETRY,
): ThroughputChartModel {
  const { days, bucket, series } = normalizeHistory(history, fallbackBucket);
  if (series.length === 0) {
    return {
      empty: true,
      bucket,
      days,
      yMax: 1,
      yTicks: [],
      xTicks: [],
      bars: [],
      summary: {
        totalCompleted: 0,
        totalOpened: 0,
        averagePerBucket: 0,
        peakCompleted: 0,
        peakLabel: "",
        bucketCount: 0,
        bucket,
      },
    };
  }

  const plotWidth = geom.width - geom.padLeft - geom.padRight;
  const plotHeight = geom.height - geom.padTop - geom.padBottom;
  const n = series.length;
  const slotWidth = plotWidth / n;
  const barWidth = Math.min(42, Math.max(4, slotWidth * 0.65));

  let totalCompleted = 0;
  let totalOpened = 0;
  let peakCompleted = 0;
  let peakLabel = formatBucketLabel(series[0].bucket_start);

  const labels: string[] = [];
  for (const pt of series) {
    const label = formatBucketLabel(pt.bucket_start);
    labels.push(label);
    totalCompleted += pt.completed;
    totalOpened += pt.opened;
    if (pt.completed > peakCompleted) {
      peakCompleted = pt.completed;
      peakLabel = label;
    }
  }

  const { yMax, yTicks } = buildYAxisTicks(peakCompleted, geom);

  const bars: ThroughputBar[] = series.map((pt, index) => {
    const label = labels[index];
    const barHeight = pt.completed > 0 ? Math.max(2, (pt.completed / yMax) * plotHeight) : 0;
    const x = geom.padLeft + index * slotWidth + (slotWidth - barWidth) / 2;
    const y = geom.padTop + plotHeight - barHeight;
    const prefix = formatTooltipPrefix(label, bucket);
    return {
      index,
      bucketStart: pt.bucket_start,
      label,
      completed: pt.completed,
      opened: pt.opened,
      x,
      y,
      width: barWidth,
      height: barHeight,
      tooltip: `${prefix}: ${pt.completed} completed, ${pt.opened} opened`,
    };
  });

  const xTicks = selectXTicks(labels).map((t) => ({
    index: t.index,
    x: slotCenterX(t.index, n, geom),
    label: t.label,
  }));

  return {
    empty: false,
    bucket,
    days,
    yMax,
    yTicks,
    xTicks,
    bars,
    summary: {
      totalCompleted,
      totalOpened,
      averagePerBucket: round1(totalCompleted / n),
      peakCompleted,
      peakLabel,
      bucketCount: n,
      bucket,
    },
  };
}

/** Build cumulative-completed burn-up chart model. */
export function buildCumulativeChartModel(
  history: ProjectReportHistory | null | undefined,
  fallbackBucket: ReportHistoryBucket = DEFAULT_HISTORY_BUCKET,
  geom: ChartGeometry = CHART_GEOMETRY,
): CumulativeChartModel {
  const { days, bucket, series } = normalizeHistory(history, fallbackBucket);
  if (series.length === 0) {
    return {
      empty: true,
      bucket,
      days,
      yMax: 1,
      yTicks: [],
      xTicks: [],
      points: [],
      linePath: "",
      areaPath: "",
      summary: {
        latestTotal: 0,
        startTotal: 0,
        windowGain: 0,
        bucketCount: 0,
        bucket,
      },
    };
  }

  const plotHeight = geom.height - geom.padTop - geom.padBottom;
  const baselineY = geom.padTop + plotHeight;
  const n = series.length;

  let maxCumulative = 0;
  let windowGain = 0;
  const labels: string[] = [];
  for (const pt of series) {
    labels.push(formatBucketLabel(pt.bucket_start));
    if (pt.cumulative_completed > maxCumulative) {
      maxCumulative = pt.cumulative_completed;
    }
    windowGain += pt.completed;
  }

  const { yMax, yTicks } = buildYAxisTicks(maxCumulative, geom);

  const points: CumulativePoint[] = series.map((pt, index) => {
    const label = labels[index];
    const x = slotCenterX(index, n, geom);
    const y = baselineY - (pt.cumulative_completed / yMax) * plotHeight;
    const prefix = formatTooltipPrefix(label, bucket);
    return {
      index,
      bucketStart: pt.bucket_start,
      label,
      cumulativeCompleted: pt.cumulative_completed,
      completed: pt.completed,
      x,
      y,
      tooltip: `${prefix}: ${pt.cumulative_completed} total completed (+${pt.completed})`,
    };
  });

  const linePath = points
    .map((pt, i) => `${i === 0 ? "M" : "L"} ${fmtCoord(pt.x)} ${fmtCoord(pt.y)}`)
    .join(" ");

  const first = points[0];
  const last = points[points.length - 1];
  const areaPath =
    points.length > 1
      ? `M ${fmtCoord(first.x)} ${fmtCoord(baselineY)} ` +
        points.map((pt) => `L ${fmtCoord(pt.x)} ${fmtCoord(pt.y)}`).join(" ") +
        ` L ${fmtCoord(last.x)} ${fmtCoord(baselineY)} Z`
      : "";

  const xTicks = selectXTicks(labels).map((t) => ({
    index: t.index,
    x: slotCenterX(t.index, n, geom),
    label: t.label,
  }));

  return {
    empty: false,
    bucket,
    days,
    yMax,
    yTicks,
    xTicks,
    points,
    linePath,
    areaPath,
    summary: {
      latestTotal: last.cumulativeCompleted,
      startTotal: first.cumulativeCompleted,
      windowGain,
      bucketCount: n,
      bucket,
    },
  };
}

function renderGridAndAxesSvg(
  yTicks: readonly YTick[],
  xTicks: readonly XTick[],
  geom: ChartGeometry = CHART_GEOMETRY,
): string {
  const xStart = geom.padLeft;
  const xEnd = geom.width - geom.padRight;
  const xLabelY = geom.height - 10;

  const gridLines = yTicks
    .map((t) => {
      const dash = t.value === 0 ? "" : ` stroke-dasharray="2 3"`;
      return (
        `<line x1="${fmtCoord(xStart)}" y1="${fmtCoord(t.y)}" x2="${fmtCoord(xEnd)}" y2="${fmtCoord(t.y)}"` +
        ` stroke-width="1"${dash} class="${CHART_GRID_STROKE}"></line>` +
        `<text x="${fmtCoord(xStart - 6)}" y="${fmtCoord(t.y + 3.5)}" text-anchor="end"` +
        ` font-size="11" class="${CHART_AXIS_TEXT_FILL} tabular-nums">${escapeSvgText(t.label)}</text>`
      );
    })
    .join("");

  const xAxisLabels = xTicks
    .map(
      (t) =>
        `<text x="${fmtCoord(t.x)}" y="${fmtCoord(xLabelY)}" text-anchor="middle"` +
        ` font-size="11" class="${CHART_AXIS_TEXT_FILL}">${escapeSvgText(t.label)}</text>`,
    )
    .join("");

  return `${gridLines}${xAxisLabels}`;
}

/** Render throughput bars + grid/axes into SVG inner markup (for <g x-html>). */
export function renderThroughputSvgInner(
  model: ThroughputChartModel,
  geom: ChartGeometry = CHART_GEOMETRY,
): string {
  if (model.empty) return "";
  const axes = renderGridAndAxesSvg(model.yTicks, model.xTicks, geom);
  const bars = model.bars
    .filter((b) => b.height > 0)
    .map(
      (b) =>
        `<rect data-chart-bar="${b.index}" x="${fmtCoord(b.x)}" y="${fmtCoord(b.y)}"` +
        ` width="${fmtCoord(b.width)}" height="${fmtCoord(b.height)}" rx="2"` +
        ` class="${THROUGHPUT_BAR_FILL}">` +
        `<title>${escapeSvgText(b.tooltip)}</title></rect>`,
    )
    .join("");
  return `${axes}${bars}`;
}

/** Render cumulative-completed area, line, and points into SVG inner markup. */
export function renderCumulativeSvgInner(
  model: CumulativeChartModel,
  geom: ChartGeometry = CHART_GEOMETRY,
): string {
  if (model.empty) return "";
  const axes = renderGridAndAxesSvg(model.yTicks, model.xTicks, geom);
  const area = model.areaPath
    ? `<path d="${model.areaPath}" class="${CUMULATIVE_AREA_FILL}"></path>`
    : "";
  const line = model.linePath
    ? `<path data-chart-line="cumulative" d="${model.linePath}" fill="none" stroke-width="2.5"` +
      ` stroke-linecap="round" stroke-linejoin="round" class="${CUMULATIVE_LINE_STROKE}"></path>`
    : "";
  const lastIdx = model.points.length - 1;
  const dots = model.points
    .map((pt, i) => {
      const r = i === lastIdx ? "4" : "2.5";
      return (
        `<circle data-chart-point="${pt.index}" cx="${fmtCoord(pt.x)}" cy="${fmtCoord(pt.y)}" r="${r}"` +
        ` stroke-width="1.5" class="${CUMULATIVE_DOT_CLASSES}">` +
        `<title>${escapeSvgText(pt.tooltip)}</title></circle>`
      );
    })
    .join("");
  return `${axes}${area}${line}${dots}`;
}

/** Accessible description for the throughput SVG (role="img"). */
export function throughputAriaLabel(model: ThroughputChartModel): string {
  if (model.empty) {
    return "Throughput chart: no task history data available.";
  }
  const { totalCompleted, bucketCount, bucket, peakCompleted, peakLabel, averagePerBucket } =
    model.summary;
  const unit = bucket === "week" ? "weeks" : "days";
  const peakClause = peakCompleted > 0 ? ` Peak ${peakCompleted} on ${peakLabel}.` : "";
  return (
    `Throughput bar chart by ${bucket}: ${totalCompleted} completed across ${bucketCount} ${unit},` +
    ` average ${averagePerBucket} per ${bucket}.${peakClause}`
  );
}

/** Accessible description for the cumulative-completed SVG (role="img"). */
export function cumulativeAriaLabel(model: CumulativeChartModel): string {
  if (model.empty) {
    return "Cumulative completed chart: no task history data available.";
  }
  const { latestTotal, windowGain, bucket } = model.summary;
  return (
    `Cumulative completed tasks line chart by ${bucket}: ${latestTotal} total completed,` +
    ` ${windowGain} completed in the last ${model.days} days.`
  );
}
