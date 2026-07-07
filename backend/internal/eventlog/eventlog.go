// Package eventlog — durable append-only лог событий с монотонным seq и live-подпиской.
//
// Назначение — несущий примитив durable ACP-демона: демон журналит сюда каждое AG-UI
// событие сессии, а brigade (реплеебл-клиент) читает по offset. Ключевое отличие от
// прежнего in-memory буфера acp.Client (срез history, seq=len) — события АДРЕСУЕМЫ по
// монотонному seq и ПЕРСИСТЯТСЯ на диск (JSONL), поэтому переживают и рестарт brigade
// (демон жив), и смерть контейнера (демон на старте читает журнал, seq продолжается), и
// позволяют дочитать пропущенное после обрыва: ReadFrom(from_seq).
//
// Полезная нагрузка хранится как json.RawMessage (для ACP-демона — JSON одного agui.Event),
// поэтому пакет не зависит от таксономии событий.
package eventlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Entry — одно журналируемое событие: монотонный seq (1-based) + JSON-нагрузка.
type Entry struct {
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// Log — durable append-only журнал одной сессии.
type Log struct {
	mu      sync.Mutex
	f       *os.File // открыт на append; nil для in-memory (path == "")
	events  []Entry  // все события в памяти (быстрый ReadFrom/проекции)
	lastSeq int64
	updated chan struct{} // закрывается и пересоздаётся на каждом Append (broadcast)
	closed  bool
}

// Open открывает журнал по пути (создаёт каталог и файл при отсутствии) и восстанавливает
// состояние из существующего файла: события читаются в память, lastSeq продолжается.
// Пустой path — журнал только в памяти (для тестов/эфемерных случаев).
func Open(path string) (*Log, error) {
	l := &Log{updated: make(chan struct{})}
	if path == "" {
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir: %w", err)
	}
	// Читаем существующий журнал (если есть), чтобы продолжить seq после рестарта.
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // строки-события могут быть крупными
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue // битая строка (недописанная при крэше) — пропускаем
			}
			l.events = append(l.events, e)
			l.lastSeq = e.Seq
		}
		_ = f.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	l.f = f
	return l, nil
}

// Append присваивает событию следующий seq, персистит его (JSONL-строкой) и оповещает
// подписчиков. Возвращает присвоенный seq.
func (l *Log) Append(data json.RawMessage) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, fmt.Errorf("eventlog: closed")
	}
	l.lastSeq++
	e := Entry{Seq: l.lastSeq, Data: data}
	if l.f != nil {
		line, err := json.Marshal(e)
		if err != nil {
			l.lastSeq--
			return 0, fmt.Errorf("eventlog: marshal: %w", err)
		}
		if _, err := l.f.Write(append(line, '\n')); err != nil {
			l.lastSeq--
			return 0, fmt.Errorf("eventlog: write: %w", err)
		}
	}
	l.events = append(l.events, e)
	// broadcast: будим всех, кто ждёт новых событий.
	close(l.updated)
	l.updated = make(chan struct{})
	return e.Seq, nil
}

// ReadFrom возвращает копию событий с seq строго больше fromSeq (fromSeq=0 → все).
func (l *Log) ReadFrom(fromSeq int64) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	// events отсортированы по seq (append-only), ищем первый с Seq > fromSeq.
	i := 0
	for i < len(l.events) && l.events[i].Seq <= fromSeq {
		i++
	}
	out := make([]Entry, len(l.events)-i)
	copy(out, l.events[i:])
	return out
}

// Updated возвращает канал, закрывающийся при следующем Append. Паттерн подписки без
// потери событий: снять канал ДО ReadFrom, затем ждать на нём (см. пример в Follow).
func (l *Log) Updated() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.updated
}

// LastSeq — текущий максимальный seq (0, если журнал пуст).
func (l *Log) LastSeq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastSeq
}

// Follow вызывает fn для каждого события начиная с fromSeq (seq > fromSeq) и далее по мере
// поступления, пока не отменят done или fn не вернёт ошибку. Корректно относительно гонки
// «append между чтением и ожиданием»: канал Updated снимается ДО ReadFrom.
func (l *Log) Follow(done <-chan struct{}, fromSeq int64, fn func(Entry) error) error {
	last := fromSeq
	for {
		ch := l.Updated() // снять ДО ReadFrom — иначе пропущенный wakeup
		for _, e := range l.ReadFrom(last) {
			if err := fn(e); err != nil {
				return err
			}
			last = e.Seq
		}
		select {
		case <-ch:
			// появились новые события — следующая итерация их вычитает
		case <-done:
			return nil
		}
	}
}

// Reset очищает журнал (файл и память, seq с нуля) и будит подписчиков. Нужен на respawn
// контейнера после его смерти с session/load: адаптер реплеит переписку заново, а старые
// записи в durable-volume стали бы дублем. На обычном reconnect (контейнер жив) НЕ зовётся.
func (l *Log) Reset() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
	l.lastSeq = 0
	if l.f != nil {
		if err := l.f.Truncate(0); err != nil {
			return fmt.Errorf("eventlog: truncate: %w", err)
		}
	}
	close(l.updated)
	l.updated = make(chan struct{})
	return nil
}

// Close закрывает файл журнала. Идемпотентно.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}
