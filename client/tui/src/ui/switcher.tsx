// Switcher — the model/mode switcher (ADR-0013 §1; ADR-0007 lifecycle verbs;
// ADR-0040 observability). The mode select picks the turn's mode; the model
// select drives the write side of lifecycle: selecting a different model
// starts it and — only once it reports up — stops the previous one
// (serving-control.feature "TUI switches models"). Models render the daemon's
// live state from the /fleet slice; a control-plane outage is a labeled banner,
// never a silent freeze (fleet-observability.feature).
import type { AppStore } from "../state/store";

export function Switcher(props: {
  store: AppStore;
  modeName: string;
  onModeChange: (name: string) => void;
}) {
  const s = () => props.store.state();
  const store = props.store;

  const modeOptions = () =>
    s().modes.map((m) => ({
      name: m.name,
      description: m.defaultModel ?? "",
    }));

  const modelOptions = () =>
    s().fleet.models.map((m) => ({
      name: `${m.name} [${m.liveState}]`,
      description: m.baseUrl ?? "",
    }));

  const currentUp = () =>
    s().fleet.models.find((m) => m.liveState === "up")?.name ?? "";

  const onSelectModel = (index: number) => {
    const selected = s().fleet.models[index];
    if (!selected || selected.name === currentUp()) return;
    void store.switchModel(currentUp(), selected.name);
  };

  const onSelectMode = (index: number) => {
    const selected = s().modes[index];
    if (selected) props.onModeChange(selected.name);
  };

  return (
    <box flexDirection="column" gap={0}>
      <box flexDirection="row" gap={1}>
        <box border title="mode" width="30%" paddingX={1}>
          <select
            options={modeOptions()}
            onSelect={(index) => onSelectMode(index)}
          />
        </box>
        <box border title="model — select to start/switch" flexGrow={1} paddingX={1}>
          <select
            options={modelOptions()}
            onSelect={(index) => onSelectModel(index)}
          />
        </box>
      </box>
      {s().fleet.busy !== null && (
        <text fg="#79C0FF">{s().fleet.busy} — serving command in flight…</text>
      )}
      {s().fleet.control === "unreachable" && (
        <text fg="#FFA657">serving control unavailable — showing last known models</text>
      )}
      {s().fleet.error && <text fg="#FF7B72">{s().fleet.error}</text>}
    </box>
  );
}
