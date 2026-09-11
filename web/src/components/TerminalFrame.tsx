export default function TerminalFrame({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="terminal">
      <div className="terminalbar">
        <div>
          <i />
          <i />
          <i />
        </div>
        <span>{title}</span>
      </div>
      <div className="terminalbody">{children}</div>
    </section>
  );
}
