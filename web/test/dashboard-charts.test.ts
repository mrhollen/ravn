import test from "node:test";
import assert from "node:assert/strict";

import {
  buildCumulativeChartModel,
  buildThroughputChartModel,
  CHART_GEOMETRY,
  cumulativeAriaLabel,
  DEFAULT_HISTORY_BUCKET,
  DEFAULT_HISTORY_DAYS,
  escapeSvgText,
  formatBucketLabel,
  isHistoryEmpty,
  niceYScale,
  normalizeHistory,
  renderCumulativeSvgInner,
  renderThroughputSvgInner,
  selectXTicks,
  throughputAriaLabel,
} from "../src/modules/dashboard-charts.ts";
import type { ProjectReportHistory } from "../src/types.ts";

function sampleDailyHistory(overrides: Partial<ProjectReportHistory> = {}): ProjectReportHistory {
  return {
    project_id: "proj-1",
    days: 30,
    bucket: "day",
    series: [
      { bucket_start: "2026-11-10T00:00:00Z", opened: 2, completed: 0, cumulative_completed: 3 },
      { bucket_start: "2026-11-11T00:00:00Z", opened: 1, completed: 2, cumulative_completed: 5 },
      { bucket_start: "2026-11-12T00:00:00Z", opened: 0, completed: 1, cumulative_completed: 6 },
      { bucket_start: "2026-11-13T00:00:00Z", opened: 3, completed: 4, cumulative_completed: 10 },
      { bucket_start: "2026-11-14T00:00:00Z", opened: 1, completed: 0, cumulative_completed: 10 },
      { bucket_start: "2026-11-15T00:00:00Z", opened: 0, completed: 2, cumulative_completed: 12 },
      { bucket_start: "2026-11-16T00:00:00Z", opened: 2, completed: 1, cumulative_completed: 13 },
    ],
    ...overrides,
  };
}

test("DashboardCharts: isHistoryEmpty and normalizeHistory handle null, empty, and malformed inputs", () => {
  assert.equal(isHistoryEmpty(null), true);
  assert.equal(isHistoryEmpty(undefined), true);
  assert.equal(
    isHistoryEmpty({ project_id: "p", days: 30, bucket: "day", series: [] }),
    true,
  );
  assert.equal(isHistoryEmpty(sampleDailyHistory()), false);

  const normNull = normalizeHistory(null, "week");
  assert.equal(normNull.bucket, "week");
  assert.equal(normNull.days, DEFAULT_HISTORY_DAYS);
  assert.deepEqual(normNull.series, []);

  const normBad = normalizeHistory(
    {
      project_id: "p",
      days: -5,
      bucket: "invalid" as unknown as "day",
      series: [
        {
          bucket_start: 123 as unknown as string,
          opened: Number.NaN,
          completed: -3,
          cumulative_completed: 4.9,
        },
      ],
    },
    DEFAULT_HISTORY_BUCKET,
  );
  assert.equal(normBad.bucket, "day");
  assert.equal(normBad.days, 30);
  assert.deepEqual(normBad.series, [
    { bucket_start: "", opened: 0, completed: 0, cumulative_completed: 4 },
  ]);
});

test("DashboardCharts: formatBucketLabel & escapeSvgText format UTC dates and escape XML safely", () => {
  assert.equal(formatBucketLabel("2026-11-16T00:00:00Z"), "Nov 16");
  assert.equal(formatBucketLabel("2026-01-05T00:00:00Z"), "Jan 5");
  assert.equal(formatBucketLabel("not-a-date"), "not-a-date");
  assert.equal(formatBucketLabel(""), "—");

  assert.equal(
    escapeSvgText(`<script>"'&`),
    "&lt;script&gt;&quot;&#39;&amp;",
  );
});

test("DashboardCharts: niceYScale produces integer ticks covering max values cleanly", () => {
  assert.deepEqual(niceYScale(0), { yMax: 1, ticks: [0, 1] });
  assert.deepEqual(niceYScale(-2), { yMax: 1, ticks: [0, 1] });
  assert.deepEqual(niceYScale(3), { yMax: 3, ticks: [0, 1, 2, 3] });
  assert.deepEqual(niceYScale(4), { yMax: 4, ticks: [0, 1, 2, 3, 4] });
  assert.deepEqual(niceYScale(7), { yMax: 8, ticks: [0, 2, 4, 6, 8] });
  assert.deepEqual(niceYScale(18), { yMax: 20, ticks: [0, 5, 10, 15, 20] });
  assert.deepEqual(niceYScale(38), { yMax: 40, ticks: [0, 10, 20, 30, 40] });
  assert.deepEqual(niceYScale(85), { yMax: 100, ticks: [0, 50, 100] });
});

test("DashboardCharts: selectXTicks handles empty, short, and long series while preserving endpoints", () => {
  assert.deepEqual(selectXTicks([]), []);
  assert.deepEqual(selectXTicks(["Nov 1", "Nov 2"]), [
    { index: 0, label: "Nov 1" },
    { index: 1, label: "Nov 2" },
  ]);

  const labels = Array.from({ length: 30 }, (_, i) => `D${i + 1}`);
  const ticks = selectXTicks(labels, 6);
  assert.equal(ticks.length, 6);
  assert.equal(ticks[0].index, 0);
  assert.equal(ticks[0].label, "D1");
  assert.equal(ticks[ticks.length - 1].index, 29);
  assert.equal(ticks[ticks.length - 1].label, "D30");
});

test("DashboardCharts: buildThroughputChartModel computes geometry, scaling, and summary for empty & populated series", () => {
  const emptyModel = buildThroughputChartModel(null);
  assert.equal(emptyModel.empty, true);
  assert.deepEqual(emptyModel.bars, []);
  assert.equal(renderThroughputSvgInner(emptyModel), "");
  assert.match(throughputAriaLabel(emptyModel), /no task history data/i);

  const model = buildThroughputChartModel(sampleDailyHistory());
  assert.equal(model.empty, false);
  assert.equal(model.yMax, 4);
  assert.equal(model.bars.length, 7);
  assert.deepEqual(model.summary, {
    totalCompleted: 10,
    totalOpened: 9,
    averagePerBucket: 1.4,
    peakCompleted: 4,
    peakLabel: "Nov 13",
    bucketCount: 7,
    bucket: "day",
  });

  // Zero-completed bucket has height 0; peak bucket reaches top of plot area.
  assert.equal(model.bars[0].height, 0);
  const peakBar = model.bars[3];
  assert.equal(peakBar.completed, 4);
  assert.equal(peakBar.y, CHART_GEOMETRY.padTop);
  assert.ok(peakBar.width >= 4 && peakBar.width <= 42);
  assert.match(peakBar.tooltip, /Nov 13: 4 completed, 3 opened/);

  // Non-zero SVG output includes grid lines, axis labels, and bars with <title>.
  const svg = renderThroughputSvgInner(model);
  assert.match(svg, /data-chart-bar="3"/);
  assert.doesNotMatch(svg, /data-chart-bar="0"/);
  assert.match(svg, /<title>Nov 13: 4 completed, 3 opened<\/title>/);
  assert.match(svg, /fill-emerald-500 dark:fill-emerald-400/);
  assert.match(
    throughputAriaLabel(model),
    /Throughput bar chart by day: 10 completed across 7 days, average 1\.4 per day\. Peak 4 on Nov 13\./,
  );
});

test("DashboardCharts: buildCumulativeChartModel computes running-total line, area, weekly labels, and single-point fallback", () => {
  const emptyModel = buildCumulativeChartModel(undefined);
  assert.equal(emptyModel.empty, true);
  assert.equal(emptyModel.linePath, "");
  assert.equal(emptyModel.areaPath, "");
  assert.equal(renderCumulativeSvgInner(emptyModel), "");
  assert.match(cumulativeAriaLabel(emptyModel), /no task history data/i);

  const weekly = sampleDailyHistory({
    bucket: "week",
    days: 30,
    series: [
      { bucket_start: "2026-11-02T00:00:00Z", opened: 4, completed: 2, cumulative_completed: 5 },
      { bucket_start: "2026-11-09T00:00:00Z", opened: 2, completed: 3, cumulative_completed: 8 },
      { bucket_start: "2026-11-16T00:00:00Z", opened: 1, completed: 4, cumulative_completed: 12 },
    ],
  });
  const model = buildCumulativeChartModel(weekly);
  assert.equal(model.empty, false);
  assert.equal(model.bucket, "week");
  assert.equal(model.yMax, 15);
  assert.deepEqual(model.summary, {
    latestTotal: 12,
    startTotal: 5,
    windowGain: 9,
    bucketCount: 3,
    bucket: "week",
  });
  assert.match(model.linePath, /^M .+ L .+ L .+$/);
  assert.match(model.areaPath, /^M .+ L .+ Z$/);
  assert.match(model.points[0].tooltip, /Week of Nov 2: 5 total completed \(\+2\)/);

  const svg = renderCumulativeSvgInner(model);
  assert.match(svg, /data-chart-line="cumulative"/);
  assert.match(svg, /data-chart-point="2"/);
  assert.match(svg, /r="4"/);
  assert.match(svg, /stroke-indigo-600 dark:stroke-indigo-400/);
  assert.match(
    cumulativeAriaLabel(model),
    /Cumulative completed tasks line chart by week: 12 total completed, 9 completed in the last 30 days\./,
  );

  // Single-point series omits areaPath and renders centered dot without NaN.
  const single = buildCumulativeChartModel({
    project_id: "proj-1",
    days: 30,
    bucket: "day",
    series: [{ bucket_start: "2026-11-16T00:00:00Z", opened: 1, completed: 0, cumulative_completed: 0 }],
  });
  assert.equal(single.areaPath, "");
  assert.equal(single.points.length, 1);
  assert.ok(Number.isFinite(single.points[0].x) && Number.isFinite(single.points[0].y));
  const singleSvg = renderCumulativeSvgInner(single);
  assert.match(singleSvg, /data-chart-point="0"/);
  const singleThroughput = buildThroughputChartModel({
    project_id: "proj-1",
    days: 30,
    bucket: "day",
    series: [{ bucket_start: "2026-11-16T00:00:00Z", opened: 1, completed: 0, cumulative_completed: 0 }],
  });
  assert.doesNotMatch(throughputAriaLabel(singleThroughput), /Peak/);
});
