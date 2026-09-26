import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { TurnEventSchema, type TurnEvent } from "./gen/blitz/v1/turn_pb";
import { applyEvent, failed, summarizeArgs, type Entry } from "./turns";

const text = (t: string, opts: { partial?: boolean; repeat?: boolean; thought?: boolean } = {}): TurnEvent =>
  create(TurnEventSchema, { author: "blitz", kind: { case: "text", value: { text: t, ...opts } } });
const call = (id: string, name: string, args = {}, partial = false): TurnEvent =>
  create(TurnEventSchema, { kind: { case: "toolCall", value: { id, name, args, partial } } });
const result = (id: string, name: string, res = {}): TurnEvent =>
  create(TurnEventSchema, { kind: { case: "toolResult", value: { id, name, result: res } } });
const finished = (message = ""): TurnEvent =>
  create(TurnEventSchema, {
    kind: { case: "finished", value: message ? { error: { reason: "RUN_FAILED", message } } : {} },
  });

const run = (events: TurnEvent[]) => events.reduce<Entry[]>(applyEvent, []);

describe("applyEvent", () => {
  it("shows streamed text once", () => {
    const got = run([text("Hel", { partial: true }), text("lo", { partial: true }), text("Hello", { repeat: true }), text(" again")]);
    expect(got).toEqual([
      { kind: "model", text: "Hello", author: "blitz", open: false },
      { kind: "model", text: " again", author: "blitz", open: false },
    ]);
  });

  it("leaves out thoughts and streamed copies of tool calls", () => {
    const got = run([text("hmm", { thought: true }), call("1", "read_file", { path: "a" }, true), call("1", "read_file", { path: "a" })]);
    expect(got).toEqual([{ kind: "tool", id: "1", name: "read_file", args: { path: "a" } }]);
  });

  it("matches results to their calls", () => {
    const got = run([call("1", "read_file"), call("2", "list_files"), result("1", "read_file", { content: "x" })]);
    expect(got[0]).toMatchObject({ id: "1", result: { content: "x" } });
    expect(got[1]).not.toHaveProperty("result");
  });

  it("ends a turn by closing open text and noting an error", () => {
    const got = run([text("partial", { partial: true }), finished("the run reached its cost limit")]);
    expect(got).toEqual([
      { kind: "model", text: "partial", author: "blitz", open: false },
      { kind: "notice", text: "the run reached its cost limit", tone: "error" },
    ]);
  });
});

describe("summaries", () => {
  it("summarizes arguments and spots failures", () => {
    expect(summarizeArgs({ path: "src/main.go" })).toBe("src/main.go");
    expect(summarizeArgs({ command: "x".repeat(100) })).toHaveLength(80);
    expect(failed({ error: "nope" })).toBe(true);
    expect(failed({ success: true })).toBe(false);
  });
});
